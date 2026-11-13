package export

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"researchos/internal/foundation/ids"
	fpaths "researchos/internal/foundation/paths"
	"researchos/internal/foundation/projects"
	"researchos/internal/foundation/schema"
	fworkspace "researchos/internal/foundation/workspace"
)

const (
	// planTTL is the §5.9.8 plan lifetime.
	planTTL = 5 * time.Minute
	// lockProbeTimeout bounds the Plan-time lock probe: a normal edit releases
	// the write lock in milliseconds, a migration holds it much longer.
	lockProbeTimeout = 2 * time.Second
	// stagingPrefix names the temporary bundle directory inside DestDir.
	stagingPrefix = ".researchos-export-"
)

// planState carries the private parts of a plan that must not leak through the
// public Plan value: the project row, the resolved base name and the paper
// projections used by the manifest and the meta JSON.
type planState struct {
	plan    *Plan
	project projects.Project
	base    string
	papers  []plannedPaper
}

// plannedPaper is one linked paper as seen by the exporter.
type plannedPaper struct {
	ID           string
	ZoteroKey    string
	FileHash     string
	RecordedPath string
	BundlePath   string // empty when missing
	Metadata     []byte
	Bytes        int64
	Missing      bool
}

// Plan performs the §5.9 preflight. It creates nothing except the default
// exports directory and a temporary writability probe inside DestDir.
func (s *Service) Plan(ctx context.Context, req Request) (*Plan, error) {
	if err := s.checkReady(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ids.Valid(req.ProjectID) {
		return nil, fmt.Errorf("export: invalid project id %q", req.ProjectID)
	}
	if !req.Format.valid() {
		return nil, fmt.Errorf("export: unknown format %q", string(req.Format))
	}
	actor, err := normalizeActor(req.Actor)
	if err != nil {
		return nil, err
	}
	req.Actor = actor

	if s.isBusy() {
		return nil, ErrBusy
	}
	if err := s.probeWriteLock(ctx, req.ProjectID); err != nil {
		return nil, err
	}

	project, err := projects.New(s.ws.Global().DB(), s.clock).Get(ctx, req.ProjectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("export: project %s does not exist: %w", req.ProjectID, err)
		}
		return nil, fmt.Errorf("export: read project %s: %w", req.ProjectID, err)
	}

	p := s.ws.Paths()
	projectDir, err := p.Abs(p.ProjectDir(req.ProjectID))
	if err != nil {
		return nil, fmt.Errorf("export: project %s: %w", req.ProjectID, err)
	}
	if info, err := os.Stat(projectDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("export: project %s has no readable data directory %s", req.ProjectID, projectDir)
	}

	destDir, err := s.resolveDestDir(req.DestDir, projectDir)
	if err != nil {
		return nil, err
	}

	rawBase := req.BaseName
	if strings.TrimSpace(rawBase) == "" {
		rawBase = project.Name
	}
	base, outputPath, err := s.resolveOutputName(destDir, sanitizeBase(rawBase, req.ProjectID), req.Format)
	if err != nil {
		return nil, err
	}

	planID, err := ids.New(s.clock)
	if err != nil {
		return nil, fmt.Errorf("export: generate plan id: %w", err)
	}
	plan := &Plan{
		ID:          planID,
		Request:     req,
		OutputPath:  outputPath,
		StagingPath: filepath.Join(destDir, stagingPrefix+planID),
		ExpiresAt:   s.clock.Now().Add(planTTL),
	}
	if plan.ExpiresAt.Before(s.clock.Now()) {
		plan.ExpiresAt = s.clock.Now().Add(planTTL)
	}

	st := &planState{plan: plan, project: project, base: base}
	if err := s.collect(ctx, st, projectDir); err != nil {
		return nil, err
	}

	if free, _, ok := s.diskFree(destDir); ok {
		need := st.plan.TotalBytes + st.plan.TotalBytes*5/100
		if free < need {
			return nil, fmt.Errorf("%w: need %d bytes, %d available", ErrInsufficientSpace, need, free)
		}
	}

	s.mu.Lock()
	s.plans[plan.ID] = st
	s.mu.Unlock()
	return plan, nil
}

// isBusy reports an unfinished export job.
func (s *Service) isBusy() bool {
	s.mu.Lock()
	j := s.active
	s.mu.Unlock()
	if j == nil {
		return false
	}
	select {
	case <-j.done:
		return false
	default:
		return true
	}
}

// probeWriteLock reports a held workspace (migration) lock as ErrBusy. The
// short-lived project lock acquisition also serializes the plan against a
// concurrent migration that starts in between.
func (s *Service) probeWriteLock(ctx context.Context, projectID string) error {
	probeCtx, cancel := context.WithTimeout(ctx, lockProbeTimeout)
	defer cancel()
	err := s.locker.WithProjectWriteLock(probeCtx, projectID, func() error { return nil })
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("%w: %v", ErrBusy, err)
}

// resolveDestDir validates DestDir and returns its resolved absolute path. An
// empty DestDir means <workspace>/exports, which Init already created; a
// missing directory is recreated because SPEC-01 layout allows a user to remove
// it (§5.9 step 2 is the only place export creates it).
func (s *Service) resolveDestDir(destDir, projectDir string) (string, error) {
	dest := destDir
	if strings.TrimSpace(dest) == "" {
		dest = filepath.Join(s.ws.Root(), filepath.FromSlash(s.ws.Paths().ExportsDir()))
		if err := os.MkdirAll(dest, fpaths.DirMode); err != nil {
			return "", fmt.Errorf("export: create default export directory: %w", err)
		}
	} else if !filepath.IsAbs(dest) {
		return "", fmt.Errorf("export: destination %q must be an absolute path", dest)
	}

	info, err := os.Stat(dest)
	if err != nil {
		return "", fmt.Errorf("export: destination %s: %w", dest, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("export: destination %s is not a directory", dest)
	}
	resolved, err := filepath.EvalSymlinks(dest)
	if err != nil {
		return "", fmt.Errorf("export: resolve destination %s: %w", dest, err)
	}

	// Writability probe: a temp file proves both create and remove rights.
	probe, err := os.CreateTemp(resolved, ".researchos-export-probe-*")
	if err != nil {
		return "", fmt.Errorf("export: destination %s is not writable: %w", resolved, err)
	}
	probePath := probe.Name()
	closeErr := probe.Close()
	if closeErr != nil {
		_ = os.Remove(probePath)
		return "", fmt.Errorf("export: destination %s is not writable: %w", resolved, closeErr)
	}
	if err := os.Remove(probePath); err != nil {
		return "", fmt.Errorf("export: destination %s is not writable: %w", resolved, err)
	}

	if sameOrInside(projectDir, resolved) {
		return "", fmt.Errorf("%w: %s", ErrDestInsideProject, resolved)
	}
	return resolved, nil
}

// resolveOutputName appends -2..-100 until the output path is free (§5.9.4).
// It never overwrites: the existence check is repeated at finalize.
func (s *Service) resolveOutputName(destDir, base string, format Format) (string, string, error) {
	ext := format.extension()
	for i := range 100 {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i+1)
		}
		out := filepath.Join(destDir, candidate+ext)
		_, err := os.Lstat(out)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return candidate, out, nil
		case err != nil:
			return "", "", fmt.Errorf("export: inspect %s: %w", out, err)
		}
	}
	return "", "", fmt.Errorf("%w: %s", ErrNameConflict, filepath.Join(destDir, base+ext))
}

// collect builds the file list, counts and preflight diagnostics.
func (s *Service) collect(ctx context.Context, st *planState, projectDir string) error {
	handle, err := s.ws.Project(ctx, st.project.ID)
	if err != nil {
		return fmt.Errorf("export: open project database: %w", err)
	}
	c := &collector{
		svc:        s,
		st:         st,
		p:          s.ws.Paths(),
		handle:     handle,
		projectDir: projectDir,
		prefix:     s.ws.Paths().ProjectDir(st.project.ID) + "/",
		seen:       make(map[string]bool),
	}
	return c.run(ctx)
}

// collector is the planning accumulator.
type collector struct {
	svc        *Service
	st         *planState
	p          fpaths.Paths
	handle     *fworkspace.ProjectHandle
	projectDir string
	prefix     string
	seen       map[string]bool
}

// run executes the §5.9.5 collection in a deterministic order: database, notes,
// attachments, papers, generated meta files.
func (c *collector) run(ctx context.Context) error {
	plan := c.st.plan
	if err := c.addProjectDB(); err != nil {
		return err
	}
	noteIDs, err := c.addNotes(ctx)
	if err != nil {
		return err
	}
	if err := c.addAttachments(ctx, noteIDs); err != nil {
		return err
	}
	if err := c.addPapers(ctx); err != nil {
		return err
	}
	c.addMeta()
	sortWarnings(plan.Warnings)
	sortMissing(plan.Missing)
	return nil
}

// addProjectDB plans the consistency snapshot.
func (c *collector) addProjectDB() error {
	rel := c.p.ProjectDB(c.st.project.ID)
	abs, err := c.p.Abs(rel)
	if err != nil {
		return fmt.Errorf("export: project database: %w", err)
	}
	var size int64
	if info, err := os.Stat(abs); err == nil {
		size = info.Size()
	}
	if info, err := os.Stat(abs + "-wal"); err == nil {
		size += info.Size()
	}
	plan := c.st.plan
	plan.Files = append(plan.Files, PlannedFile{
		SourceAbs:  abs,
		BundlePath: "workspace/" + rel,
		Kind:       KindProjectDB,
		Bytes:      size,
	})
	plan.TotalBytes += size
	return nil
}

// addNotes plans every note markdown file, soft-deleted ones included. It
// returns all note ids so attachments can be walked even when the markdown is
// gone.
func (c *collector) addNotes(ctx context.Context) ([]string, error) {
	rows, err := c.handle.DB().QueryContext(ctx,
		`SELECT id, file_path FROM notes ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("export: read notes: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var (
			noteID   string
			filePath string
		)
		if err := rows.Scan(&noteID, &filePath); err != nil {
			return nil, fmt.Errorf("export: read notes: %w", err)
		}
		ids = append(ids, noteID)
		if !c.validRecordedPath(filePath) {
			c.warn("path_rejected", filePath)
			continue
		}
		abs, err := c.p.Abs(filePath)
		if err != nil {
			c.warn("path_rejected", filePath)
			continue
		}
		info, err := os.Lstat(abs)
		if err != nil {
			c.st.plan.Missing = append(c.st.plan.Missing, MissingRef{
				Kind: "note_md", Ref: noteID, RecordedPath: filePath,
			})
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			c.warn("symlink_skipped", "workspace/"+filePath)
			continue
		}
		if !info.Mode().IsRegular() {
			c.st.plan.Missing = append(c.st.plan.Missing, MissingRef{
				Kind: "note_md", Ref: noteID, RecordedPath: filePath,
			})
			continue
		}
		bundle := "workspace/" + filePath
		if c.seen[bundle] {
			continue
		}
		c.seen[bundle] = true
		c.st.plan.Files = append(c.st.plan.Files, PlannedFile{
			SourceAbs:  abs,
			BundlePath: bundle,
			Kind:       KindNoteMD,
			Bytes:      info.Size(),
		})
		c.st.plan.TotalBytes += info.Size()
		c.st.plan.NoteCount++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export: read notes: %w", err)
	}
	return ids, nil
}

// addAttachments walks attachments/<note-id>/ for every note id. A missing
// directory is a warning, not an error: notes without images are common.
func (c *collector) addAttachments(ctx context.Context, noteIDs []string) error {
	for _, noteID := range noteIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		relDir := c.p.AttachmentsDir(c.st.project.ID, noteID)
		if err := c.p.ValidateInternal(relDir); err != nil {
			c.warn("path_rejected", relDir)
			continue
		}
		absDir, err := c.p.Abs(relDir)
		if err != nil {
			c.warn("path_rejected", relDir)
			continue
		}
		info, err := os.Lstat(absDir)
		if err != nil {
			c.warn("attachment_dir_missing", "workspace/"+relDir)
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			c.warn("symlink_skipped", "workspace/"+relDir)
			continue
		}
		if !info.IsDir() {
			c.warn("attachment_dir_missing", "workspace/"+relDir)
			continue
		}
		err = filepath.WalkDir(absDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			sub, err := filepath.Rel(absDir, path)
			if err != nil {
				return err
			}
			bundle := "workspace/" + relDir + "/" + filepath.ToSlash(sub)
			if d.Type()&os.ModeSymlink != 0 {
				c.warn("symlink_skipped", bundle)
				return nil
			}
			fileInfo, err := d.Info()
			if err != nil {
				return err
			}
			if !fileInfo.Mode().IsRegular() {
				return nil
			}
			if c.seen[bundle] {
				return nil
			}
			c.seen[bundle] = true
			c.st.plan.Files = append(c.st.plan.Files, PlannedFile{
				SourceAbs:  path,
				BundlePath: bundle,
				Kind:       KindAttachment,
				Bytes:      fileInfo.Size(),
			})
			c.st.plan.TotalBytes += fileInfo.Size()
			c.st.plan.AttachmentCount++
			return nil
		})
		if err != nil {
			return fmt.Errorf("export: walk attachments of note %s: %w", noteID, err)
		}
	}
	return nil
}

// addPapers resolves the project's linked papers, loads their global rows and
// plans the local PDF copies. A PDF shared with another project is included
// because project_papers, not the library, defines membership (§4.4).
func (c *collector) addPapers(ctx context.Context) error {
	paperIDs, err := c.paperIDs(ctx)
	if err != nil {
		return err
	}
	global := c.svc.ws.Global().DB()
	for _, paperID := range paperIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		pp := plannedPaper{ID: paperID}
		var (
			zoteroKey string
			filePath  sql.NullString
			fileHash  sql.NullString
			fileSize  sql.NullInt64
			fileState string
			metadata  string
		)
		err := global.QueryRowContext(ctx,
			`SELECT zotero_key, file_path, file_hash, file_size, file_state, metadata
			   FROM papers WHERE id = ?`, paperID).
			Scan(&zoteroKey, &filePath, &fileHash, &fileSize, &fileState, &metadata)
		if errors.Is(err, sql.ErrNoRows) {
			pp.Missing = true
			pp.RecordedPath = c.p.PaperPDF(paperID)
			c.st.plan.Missing = append(c.st.plan.Missing, MissingRef{
				Kind: "paper_pdf", Ref: paperID, RecordedPath: pp.RecordedPath,
			})
			c.st.papers = append(c.st.papers, pp)
			continue
		}
		if err != nil {
			return fmt.Errorf("export: read paper %s: %w", paperID, err)
		}
		pp.ZoteroKey = zoteroKey
		pp.FileHash = fileHash.String
		pp.Metadata = []byte(metadata)
		pp.Bytes = fileSize.Int64

		recorded := filePath.String
		validRecorded := recorded == ""
		if recorded != "" {
			validRecorded = c.validLibraryPath(recorded)
			if !validRecorded {
				c.warn("path_rejected", recorded)
			}
		}
		if recorded != "" {
			pp.RecordedPath = recorded
		} else {
			pp.RecordedPath = c.p.PaperPDF(paperID)
		}
		if fileState != string(schema.PaperFileOK) || !validRecorded {
			pp.Missing = true
			c.st.plan.Missing = append(c.st.plan.Missing, MissingRef{
				Kind: "paper_pdf", Ref: paperID, RecordedPath: pp.RecordedPath,
			})
			c.st.papers = append(c.st.papers, pp)
			continue
		}

		rel := c.p.PaperPDF(paperID)
		abs, err := c.p.Abs(rel)
		if err != nil {
			c.warn("path_rejected", rel)
			pp.Missing = true
			c.st.plan.Missing = append(c.st.plan.Missing, MissingRef{
				Kind: "paper_pdf", Ref: paperID, RecordedPath: pp.RecordedPath,
			})
			c.st.papers = append(c.st.papers, pp)
			continue
		}
		info, err := os.Lstat(abs)
		if err != nil || !info.Mode().IsRegular() {
			if info != nil && info.Mode()&os.ModeSymlink != 0 {
				c.warn("symlink_skipped", "workspace/"+rel)
			}
			pp.Missing = true
			c.st.plan.Missing = append(c.st.plan.Missing, MissingRef{
				Kind: "paper_pdf", Ref: paperID, RecordedPath: pp.RecordedPath,
			})
			c.st.papers = append(c.st.papers, pp)
			continue
		}
		pp.BundlePath = "workspace/" + rel
		pp.Bytes = info.Size()
		c.st.plan.Files = append(c.st.plan.Files, PlannedFile{
			SourceAbs:  abs,
			BundlePath: pp.BundlePath,
			Kind:       KindPaperPDF,
			Bytes:      info.Size(),
		})
		c.st.plan.TotalBytes += info.Size()
		c.st.plan.PaperCount++
		c.st.papers = append(c.st.papers, pp)
	}
	return nil
}

// paperIDs returns the linked paper ids. project_papers is authoritative; the
// entity_relations dst_type='paper' fallback exists only for pre-link data
// (§7.3). The SPEC-04 port, when supplied, is preferred as the read path.
func (c *collector) paperIDs(ctx context.Context) ([]string, error) {
	if c.svc.papers != nil {
		ids, err := c.svc.papers.ListProjectPaperIDs(ctx, c.st.project.ID)
		if err == nil {
			return dedupeSorted(ids), nil
		}
	}
	var ids []string
	rows, err := c.handle.DB().QueryContext(ctx,
		`SELECT paper_id FROM project_papers WHERE project_id = ? ORDER BY paper_id`,
		c.st.project.ID)
	if err != nil {
		return nil, fmt.Errorf("export: read project_papers: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("export: read project_papers: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("export: read project_papers: %w", err)
	}
	rows.Close()
	if len(ids) > 0 {
		return dedupeSorted(ids), nil
	}

	rows, err = c.handle.DB().QueryContext(ctx,
		`SELECT DISTINCT dst_id FROM entity_relations WHERE dst_type = 'paper'`)
	if err != nil {
		return nil, fmt.Errorf("export: read paper relations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("export: read paper relations: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("export: read paper relations: %w", err)
	}
	return dedupeSorted(ids), nil
}

// addMeta plans the two generated metadata files. Their bytes are unknown until
// finalize, so they contribute nothing to TotalBytes.
func (c *collector) addMeta() {
	c.st.plan.Files = append(c.st.plan.Files,
		PlannedFile{BundlePath: "meta/project.json", Kind: KindMetaJSON},
		PlannedFile{BundlePath: "meta/papers.json", Kind: KindMetaJSON},
	)
}

// validRecordedPath accepts only canonical workspace-relative note paths that
// stay inside this project (§8.2 path safety).
func (c *collector) validRecordedPath(rel string) bool {
	if err := c.p.ValidateInternal(rel); err != nil {
		return false
	}
	return strings.HasPrefix(rel, c.prefix)
}

// validLibraryPath accepts canonical library paths for a paper copy.
func (c *collector) validLibraryPath(rel string) bool {
	if err := c.p.ValidateInternal(rel); err != nil {
		return false
	}
	return strings.HasPrefix(rel, "library/papers/")
}

// warn appends one warning.
func (c *collector) warn(code, path string) {
	c.st.plan.Warnings = append(c.st.plan.Warnings, Warning{Code: code, Path: path})
}

// dedupeSorted removes duplicate ids and sorts the result.
func dedupeSorted(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// sortWarnings orders warnings so the manifest is byte-stable.
func sortWarnings(w []Warning) {
	sort.Slice(w, func(i, j int) bool {
		if w[i].Code != w[j].Code {
			return w[i].Code < w[j].Code
		}
		return w[i].Path < w[j].Path
	})
}

// sortMissing orders missing references so the manifest is byte-stable.
func sortMissing(m []MissingRef) {
	sort.Slice(m, func(i, j int) bool {
		if m[i].Kind != m[j].Kind {
			return m[i].Kind < m[j].Kind
		}
		if m[i].Ref != m[j].Ref {
			return m[i].Ref < m[j].Ref
		}
		return m[i].RecordedPath < m[j].RecordedPath
	})
}

// sameOrInside reports whether target is root or below it.
func sameOrInside(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return !filepath.IsAbs(rel)
}
