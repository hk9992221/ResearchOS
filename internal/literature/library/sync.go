package library

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/papers"
	"researchos/internal/foundation/schema"
	"researchos/internal/literature"
	"researchos/internal/literature/zotero"
)

const syncReportTTL = 30 * time.Minute

type syncReportEntry struct {
	report    literature.SyncReport
	expiresAt time.Time
}

// Sync exposes the LIT-09 surface.
func (s *Service) Sync() literature.SyncService { return &syncAPI{s} }

type syncAPI struct{ *Service }

var (
	_ literature.SyncService        = (*syncAPI)(nil)
	_ literature.LibraryMaintenance = (*maintenanceAPI)(nil)
)

// Diff compares Zotero sources with imported copies (read-only).
func (a *syncAPI) Diff(ctx context.Context, scope literature.SyncScope, verifyLocalCopy bool) (literature.SyncReport, error) {
	report, err := a.diff(ctx, scope, verifyLocalCopy)
	if err != nil {
		return literature.SyncReport{}, err
	}
	a.syncMu.Lock()
	if a.syncReports == nil {
		a.syncReports = map[string]syncReportEntry{}
	}
	a.syncReports[report.ID] = syncReportEntry{report: report, expiresAt: a.clock.Now().Add(syncReportTTL)}
	a.syncMu.Unlock()
	return report, nil
}

func (s *Service) diff(ctx context.Context, scope literature.SyncScope, verifyLocalCopy bool) (literature.SyncReport, error) {
	if s.zotero == nil {
		return literature.SyncReport{}, literature.ErrZoteroNotConfigured
	}
	rows, err := s.scopePapers(ctx, scope)
	if err != nil {
		return literature.SyncReport{}, err
	}
	report := literature.SyncReport{
		ID:        ids.MustNew(s.clock),
		CreatedAt: s.clock.Now(),
		Scope:     scope,
	}
	for _, row := range rows {
		diff, err := s.diffOne(ctx, row, verifyLocalCopy)
		if err != nil {
			report.Errors = append(report.Errors, literature.SyncError{PaperID: row.ID, Message: err.Error()})
			continue
		}
		if diff.Changed || diff.SourceState != "ok" || diff.LocalState != "ok" {
			report.Diffs = append(report.Diffs, diff)
		} else {
			report.Unchanged++
		}
	}
	return report, nil
}

func (s *Service) scopePapers(ctx context.Context, scope literature.SyncScope) ([]papers.Paper, error) {
	if len(scope.PaperIDs) > 0 {
		out := make([]papers.Paper, 0, len(scope.PaperIDs))
		for _, id := range scope.PaperIDs {
			row, err := s.papers.Get(ctx, id)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return nil, literature.ErrPaperNotFound
				}
				return nil, err
			}
			if scope.ProjectID != "" {
				handle, err := s.ws.Project(ctx, scope.ProjectID)
				if err != nil {
					return nil, err
				}
				var n int
				if err := handle.DB().QueryRowContext(ctx,
					`SELECT count(*) FROM project_papers WHERE project_id = ? AND paper_id = ?`,
					scope.ProjectID, id).Scan(&n); err != nil {
					return nil, err
				}
				if n == 0 {
					return nil, literature.ErrProjectLinkMissing
				}
			}
			out = append(out, row)
		}
		return out, nil
	}
	if scope.ProjectID == "" {
		return s.papers.List(ctx, papers.PaperQuery{})
	}
	handle, err := s.ws.Project(ctx, scope.ProjectID)
	if err != nil {
		return nil, err
	}
	rows, err := handle.DB().QueryContext(ctx,
		`SELECT paper_id FROM project_papers WHERE project_id = ? ORDER BY linked_at, paper_id`, scope.ProjectID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]papers.Paper, 0, len(ids))
	for _, id := range ids {
		row, err := s.papers.Get(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *Service) diffOne(ctx context.Context, row papers.Paper, verifyLocalCopy bool) (literature.SyncDiff, error) {
	md, err := decodeMetadata(row)
	if err != nil {
		return literature.SyncDiff{}, err
	}
	diff := literature.SyncDiff{
		PaperID:     row.ID,
		Title:       md.Title,
		ZoteroKey:   row.ZoteroKey,
		LibraryID:   row.ZoteroLibraryID,
		LocalHash:   row.FileHash,
		LocalState:  "ok",
		SourceState: "ok",
	}
	if row.ZoteroLibraryID == "" {
		diff.LibraryID = "0"
	}
	source, sourceErr := s.resolveSourceFile(ctx, row)
	if sourceErr != nil {
		switch {
		case errors.Is(sourceErr, literature.ErrNoPDFAttachment), errors.Is(sourceErr, literature.ErrAttachmentUnresolved), errors.Is(sourceErr, literature.ErrAttachmentChoice):
			diff.SourceState = "missing"
		default:
			diff.SourceState = "unreadable"
		}
		diff.Warnings = append(diff.Warnings, sourceErr.Error())
	} else {
		info, err := os.Stat(source.Path)
		if err != nil {
			diff.SourceState = "unreadable"
			diff.Warnings = append(diff.Warnings, err.Error())
		} else {
			diff.SourcePath = source.Path
			diff.SourceSize = info.Size()
			diff.SourceModTime = info.ModTime().UTC()
			hash, err := hashFile(ctx, source.Path)
			if err != nil {
				diff.SourceState = "unreadable"
				diff.Warnings = append(diff.Warnings, err.Error())
			} else {
				diff.SourceHash = hash
				if hash != row.FileHash {
					diff.Changed = true
				}
			}
		}
	}

	localPath, err := s.ws.Paths().Abs(row.FilePath)
	if err != nil || row.FilePath == "" {
		diff.LocalState = "missing"
		diff.Changed = true
		diff.Warnings = append(diff.Warnings, "local_file_missing")
	} else if info, err := os.Stat(localPath); err != nil {
		diff.LocalState = "missing"
		diff.Changed = true
		diff.Warnings = append(diff.Warnings, "local_file_missing")
	} else {
		diff.LocalSize = info.Size()
		diff.LocalModTime = info.ModTime().UTC()
		if verifyLocalCopy {
			hash, err := hashFile(ctx, localPath)
			if err != nil {
				diff.Warnings = append(diff.Warnings, err.Error())
			} else if row.FileHash != "" && hash != row.FileHash {
				diff.LocalState = "corrupt"
				diff.LocalCorrupted = true
				diff.Changed = true
			}
		}
	}
	return diff, nil
}

// Apply overwrites explicitly confirmed copies (MCP must confirm in-app first).
func (a *syncAPI) Apply(ctx context.Context, req literature.SyncApplyRequest) (literature.SyncApplyResult, error) {
	if req.Actor == literature.ActorMCP {
		a.syncMu.Lock()
		entry, ok := a.syncReports[req.ReportID]
		a.syncMu.Unlock()
		if !ok || a.clock.Now().After(entry.expiresAt) {
			return literature.SyncApplyResult{}, literature.ErrSyncReportExpired
		}
		return literature.SyncApplyResult{}, &literature.ConfirmationRequired{ReportID: req.ReportID, Diffs: entry.report.Diffs}
	}
	a.syncMu.Lock()
	entry, ok := a.syncReports[req.ReportID]
	if !ok || a.clock.Now().After(entry.expiresAt) {
		a.syncMu.Unlock()
		return literature.SyncApplyResult{}, literature.ErrSyncReportExpired
	}
	a.syncMu.Unlock()

	var out literature.SyncApplyResult
	for _, decision := range req.Decisions {
		if !decision.Confirm {
			out.Skipped = append(out.Skipped, decision.PaperID)
			continue
		}
		diff, ok := findDiff(entry.report.Diffs, decision.PaperID)
		if !ok {
			out.Failed = append(out.Failed, literature.SyncError{PaperID: decision.PaperID, Message: "diff not found in report"})
			continue
		}
		if err := a.applyOne(ctx, diff, decision, req.Actor); err != nil {
			switch {
			case errors.Is(err, errSyncConflict):
				out.Conflicted = append(out.Conflicted, decision.PaperID)
			default:
				out.Failed = append(out.Failed, literature.SyncError{PaperID: decision.PaperID, Message: err.Error()})
			}
			continue
		}
		out.Overwritten = append(out.Overwritten, decision.PaperID)
	}
	return out, nil
}

var errSyncConflict = errors.New("sync conflict")

func findDiff(diffs []literature.SyncDiff, paperID string) (literature.SyncDiff, bool) {
	for _, d := range diffs {
		if d.PaperID == paperID {
			return d, true
		}
	}
	return literature.SyncDiff{}, false
}

func (s *Service) applyOne(ctx context.Context, diff literature.SyncDiff, decision literature.OverwriteDecision, actor literature.Actor) error {
	if diff.SourceState != "ok" {
		return fmt.Errorf("%w: source state %s", errSyncConflict, diff.SourceState)
	}
	source, err := s.resolveSourceFileByID(ctx, diff.PaperID)
	if err != nil {
		return err
	}
	sourceHash, err := hashFile(ctx, source.Path)
	if err != nil {
		return err
	}
	if decision.ExpectedSourceHash != "" && sourceHash != decision.ExpectedSourceHash {
		return errSyncConflict
	}
	row, err := s.papers.Get(ctx, diff.PaperID)
	if err != nil {
		return err
	}
	if decision.ExpectedLocalHash != "" && row.FileHash != decision.ExpectedLocalHash {
		return errSyncConflict
	}
	if diff.LocalState == "ok" && decision.ExpectedLocalHash != "" {
		localPath, err := s.ws.Paths().Abs(row.FilePath)
		if err != nil {
			return err
		}
		localHash, err := hashFile(ctx, localPath)
		if err != nil || localHash != decision.ExpectedLocalHash {
			return errSyncConflict
		}
	}
	relPath, size, err := s.replaceCopy(ctx, row.ID, source.Path)
	if err != nil {
		return err
	}
	if err := s.ws.Global().InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE papers SET file_path = ?, file_hash = ?, file_size = ?, file_state = 'ok',
			        last_synced_at = ?, updated_at = ? WHERE id = ?`,
			relPath, sourceHash, size, clock.Format(s.clock.Now()), clock.Format(s.clock.Now()), row.ID); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	return s.emitOverwrite(ctx, row.ID, actor)
}

func (s *Service) emitOverwrite(ctx context.Context, paperID string, actor literature.Actor) error {
	rows, err := s.ws.Global().DB().QueryContext(ctx, `SELECT id FROM projects ORDER BY created_at, id`)
	if err != nil {
		return err
	}
	var projectIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		projectIDs = append(projectIDs, id)
	}
	rows.Close()
	for _, projectID := range projectIDs {
		handle, err := s.ws.Project(ctx, projectID)
		if err != nil {
			continue
		}
		var linked int
		if err := handle.DB().QueryRowContext(ctx,
			`SELECT count(*) FROM project_papers WHERE project_id = ? AND paper_id = ?`,
			projectID, paperID).Scan(&linked); err != nil || linked == 0 {
			continue
		}
		if err := handle.InTx(ctx, func(tx *sql.Tx) error {
			return handle.Events().Emit(ctx, tx, events.Event{
				Type:       schema.EventPaperOverwriteConfirmed,
				EntityType: schema.EntityPaper,
				EntityID:   paperID,
				Summary:    "确认覆盖 PDF 副本",
				Actor:      libraryActor(actor),
			})
		}); err != nil {
			return err
		}
	}
	return nil
}

// replaceCopy atomically replaces library/papers/<id>/source.pdf.
func (s *Service) replaceCopy(ctx context.Context, paperID, sourcePath string) (string, int64, error) {
	rel := s.ws.Paths().PaperPDF(paperID)
	abs, err := s.ws.Paths().Abs(filepath.Dir(rel))
	if err != nil {
		// Abs validates an existing path; the paper dir must exist for an
		// imported paper. Fall back to the raw join only for a fresh path.
		return "", 0, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", 0, err
	}
	target := filepath.Join(abs, "source.pdf")
	tmp, err := os.CreateTemp(abs, ".source.pdf.*.tmp")
	if err != nil {
		return "", 0, err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	in, err := os.Open(sourcePath)
	if err != nil {
		cleanup()
		return "", 0, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), in)
	in.Close()
	if err != nil {
		cleanup()
		return "", 0, err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", 0, err
	}
	expected, _ := hashFile(ctx, sourcePath)
	if hex.EncodeToString(h.Sum(nil)) != expected {
		_ = os.Remove(tmpName)
		return "", 0, errors.New("copied bytes do not match the source hash")
	}
	backup := target + ".bak"
	_ = os.Remove(backup)
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			_ = os.Remove(tmpName)
			return "", 0, err
		}
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Rename(backup, target)
		_ = os.Remove(tmpName)
		return "", 0, err
	}
	_ = os.Remove(backup)
	return rel, n, nil
}

func (s *Service) resolveSourceFile(ctx context.Context, row papers.Paper) (literature.ResolvedFile, error) {
	if s.zotero == nil {
		return literature.ResolvedFile{}, literature.ErrZoteroNotConfigured
	}
	reader, err := s.zotero.Reader(ctx)
	if err != nil {
		return literature.ResolvedFile{}, err
	}
	libraryID := row.ZoteroLibraryID
	if libraryID == "" {
		libraryID = "0"
	}
	attachments, err := reader.ListAttachments(ctx, libraryID, row.ZoteroKey)
	if err != nil {
		return literature.ResolvedFile{}, err
	}
	attachment, err := pickAttachment(attachments, row.AttachmentKey)
	if err != nil {
		return literature.ResolvedFile{}, err
	}
	return reader.ResolveAttachment(ctx, attachment)
}

func (s *Service) resolveSourceFileByID(ctx context.Context, paperID string) (literature.ResolvedFile, error) {
	row, err := s.papers.Get(ctx, paperID)
	if err != nil {
		return literature.ResolvedFile{}, err
	}
	return s.resolveSourceFile(ctx, row)
}

// RefreshMetadata replaces metadata without touching PDF copies.
func (a *syncAPI) RefreshMetadata(ctx context.Context, scope literature.SyncScope, actor literature.Actor) (literature.MetadataRefreshResult, error) {
	if a.zotero == nil {
		return literature.MetadataRefreshResult{}, literature.ErrZoteroNotConfigured
	}
	rows, err := a.scopePapers(ctx, scope)
	if err != nil {
		return literature.MetadataRefreshResult{}, err
	}
	reader, err := a.zotero.Reader(ctx)
	if err != nil {
		return literature.MetadataRefreshResult{}, err
	}
	result := literature.MetadataRefreshResult{RefreshedAt: a.clock.Now()}
	for _, row := range rows {
		libraryID := row.ZoteroLibraryID
		if libraryID == "" {
			libraryID = "0"
		}
		item, err := reader.GetItem(ctx, libraryID, row.ZoteroKey)
		if err != nil {
			result.Failed = append(result.Failed, literature.SyncError{PaperID: row.ID, Message: err.Error()})
			continue
		}
		md := zotero.ToPaperMetadata(item)
		raw, err := json.Marshal(md)
		if err != nil {
			result.Failed = append(result.Failed, literature.SyncError{PaperID: row.ID, Message: err.Error()})
			continue
		}
		if err := a.ws.Global().InTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				`UPDATE papers SET metadata = ?, source_state = 'ok', updated_at = ? WHERE id = ?`,
				string(raw), clock.Format(a.clock.Now()), row.ID)
			return err
		}); err != nil {
			result.Failed = append(result.Failed, literature.SyncError{PaperID: row.ID, Message: err.Error()})
			continue
		}
		result.Updated = append(result.Updated, row.ID)
	}
	return result, nil
}

// ListForExport returns the project's linked copies (no packaging).
func (a *maintenanceAPI) ListForExport(ctx context.Context, projectID string) ([]literature.PaperExportEntry, error) {
	handle, err := a.ws.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	rows, err := handle.DB().QueryContext(ctx,
		`SELECT paper_id FROM project_papers WHERE project_id = ? ORDER BY linked_at, paper_id`, projectID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := make([]literature.PaperExportEntry, 0, len(ids))
	for _, id := range ids {
		paper, err := a.papers.Get(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		md, err := decodeMetadata(paper)
		if err != nil {
			return nil, err
		}
		out = append(out, literature.PaperExportEntry{
			PaperID: id, RelativePath: paper.FilePath, FileHash: paper.FileHash, Metadata: md,
		})
	}
	return out, nil
}

// ReconcilePaper reports copy state without repairing it.
func (a *maintenanceAPI) ReconcilePaper(ctx context.Context, paperID string) (literature.ReconcileReport, error) {
	paper, err := a.papers.Get(ctx, paperID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return literature.ReconcileReport{}, literature.ErrPaperNotFound
		}
		return literature.ReconcileReport{}, err
	}
	report := literature.ReconcileReport{
		PaperID: paperID, StoredHash: paper.FileHash, SourceState: string(paper.SourceState),
	}
	abs, err := a.ws.Paths().Abs(paper.FilePath)
	if err != nil {
		return report, nil
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		return report, nil
	}
	report.FileExists = true
	actual, err := hashFile(ctx, abs)
	if err != nil {
		return report, err
	}
	report.ActualHash = actual
	report.HashMatches = actual == paper.FileHash
	return report, nil
}

// Maintenance exposes §5.10.
func (s *Service) Maintenance() literature.LibraryMaintenance { return &maintenanceAPI{s} }

type maintenanceAPI struct{ *Service }
