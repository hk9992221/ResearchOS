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
	"strings"
	"sync"

	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/papers"
	"researchos/internal/foundation/projectpapers"
	"researchos/internal/foundation/schema"
	"researchos/internal/literature"
	"researchos/internal/literature/zotero"
)

const (
	// importPageSize is the Zotero ListItems page size (§5.2 limit 1..100).
	importPageSize = 100
	// importTitleLimit truncates the title inside the event summary (§7.1: 80).
	importTitleLimit = 80
)

var _ literature.ImportService = (*Service)(nil)

// importLocks holds one mutex per workspace root so two Service instances on
// the same workspace still serialize Import (§5.4 single-flight).
var importLocks sync.Map // workspace root -> *sync.Mutex

func importLockFor(root string) *sync.Mutex {
	if existing, ok := importLocks.Load(root); ok {
		return existing.(*sync.Mutex)
	}
	created, _ := importLocks.LoadOrStore(root, &sync.Mutex{})
	return created.(*sync.Mutex)
}

// Preview expands the selection and reports what Import would do. It is
// strictly read-only: no files, rows, events or Zotero writes (§5.4).
func (s *Service) Preview(ctx context.Context, sel literature.ImportSelection) (literature.ImportPreview, error) {
	preview := literature.ImportPreview{
		Entries: make([]literature.ImportPreviewEntry, 0),
		Errors:  make([]literature.ImportError, 0),
	}
	libraryID, err := s.validateImportSelection(ctx, &sel)
	if err != nil {
		return preview, err
	}
	reader, err := s.zotero.Reader(ctx)
	if err != nil {
		return preview, fmt.Errorf("library: 预览导入: 读取 Zotero: %w", err)
	}
	items, itemErrors, err := expandImportItems(ctx, reader, libraryID, sel)
	if err != nil {
		return preview, fmt.Errorf("library: 预览导入: %w", err)
	}
	preview.Errors = append(preview.Errors, itemErrors...)
	for _, item := range items {
		entry, err := s.previewEntry(ctx, reader, libraryID, item, sel)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return preview, fmt.Errorf("library: 预览导入: %w", ctxErr)
			}
			preview.Errors = append(preview.Errors, literature.ImportError{
				ZoteroKey: item.Key,
				Message:   err.Error(),
			})
			continue
		}
		preview.Entries = append(preview.Entries, entry)
	}
	return preview, nil
}

// Import executes the preview serially. Each entry succeeds or fails on its
// own; earlier successes are never rolled back (§5.4). Progress is called after
// every processed entry, synchronously.
func (s *Service) Import(ctx context.Context, sel literature.ImportSelection, actor literature.Actor, progress literature.ProgressFunc) (literature.ImportResult, error) {
	result := literature.ImportResult{Outcomes: make([]literature.ImportOutcome, 0)}
	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("library: 导入: %w", err)
	}
	libraryID, err := s.validateImportSelection(ctx, &sel)
	if err != nil {
		return result, err
	}
	if err := validateImportActor(actor); err != nil {
		return result, err
	}
	if !s.importMu.TryLock() {
		return result, fmt.Errorf("library: 导入: %w", literature.ErrBusy)
	}
	defer s.importMu.Unlock()

	preview, err := s.Preview(ctx, sel)
	if err != nil {
		return result, err
	}
	reader, err := s.zotero.Reader(ctx)
	if err != nil {
		return result, fmt.Errorf("library: 导入: 读取 Zotero: %w", err)
	}
	total := len(preview.Entries)
	for index, entry := range preview.Entries {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, fmt.Errorf("library: 导入已取消（已完成 %d/%d）: %w", index, total, ctxErr)
		}
		outcome := s.importEntry(ctx, reader, libraryID, sel, entry, actor)
		result.Outcomes = append(result.Outcomes, outcome)
		accumulateImportResult(&result, outcome.Status)
		if outcome.PaperID != "" && (outcome.Status == literature.ImportCreated || outcome.Status == literature.ImportLinkedExisting) {
			s.notifyIndex(ctx, outcome.PaperID, schema.EventPaperImported, "导入文献")
		}
		if progress != nil {
			progress(literature.ImportProgress{
				Index:   index + 1,
				Total:   total,
				ItemKey: entry.ZoteroKey,
				Status:  outcome.Status,
				Message: importProgressMessage(outcome),
			})
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, fmt.Errorf("library: 导入已取消（已完成 %d/%d）: %w", index+1, total, ctxErr)
		}
	}
	return result, nil
}

// validateImportSelection checks the shared preconditions and returns the
// normalized library id (v1: personal library only).
func (s *Service) validateImportSelection(ctx context.Context, sel *literature.ImportSelection) (string, error) {
	if s.zotero == nil {
		return "", fmt.Errorf("library: 导入: %w", literature.ErrZoteroNotConfigured)
	}
	if _, err := s.activeProject(ctx, sel.ProjectID); err != nil {
		return "", fmt.Errorf("library: 导入: %w", err)
	}
	libraryID := strings.TrimSpace(sel.LibraryID)
	if libraryID == "" {
		libraryID = "0"
	}
	if libraryID != "0" {
		return "", fmt.Errorf("library: 导入: v1 仅支持个人库（library_id=\"0\"），收到 %q", libraryID)
	}
	return libraryID, nil
}

func validateImportActor(actor literature.Actor) error {
	switch actor {
	case literature.ActorUser, literature.ActorMCP:
		return nil
	default:
		return fmt.Errorf("library: 导入: 不支持的操作者 %q: %w", string(actor), errs.ErrBadActor)
	}
}

// previewEntry reads the attachments and existing-paper state of one item.
func (s *Service) previewEntry(ctx context.Context, reader literature.ZoteroReader, libraryID string, item literature.ZoteroItem, sel literature.ImportSelection) (literature.ImportPreviewEntry, error) {
	attachments, err := reader.ListAttachments(ctx, libraryID, item.Key)
	if err != nil {
		return literature.ImportPreviewEntry{}, fmt.Errorf("读取 Zotero 附件失败：%v", err)
	}
	meta := zotero.ToPaperMetadata(item)
	pdfs := pdfAttachments(attachments)
	entry := literature.ImportPreviewEntry{
		ZoteroKey:    item.Key,
		LibraryID:    libraryID,
		Title:        meta.Title,
		Creators:     meta.Creators,
		Year:         meta.Year,
		PDFAvailable: len(pdfs) > 0,
		PDFCount:     len(pdfs),
	}
	if choice := sel.AttachmentChoices[item.Key]; choice != "" {
		entry.AttachmentKey = choice
	}
	row, exists, err := s.papers.ByZotero(ctx, libraryID, item.Key)
	if err != nil {
		return literature.ImportPreviewEntry{}, fmt.Errorf("查询已导入文献失败：%w", err)
	}
	if exists {
		entry.ExistingPaperID = row.ID
		entry.ExistingHash = row.FileHash
		if s.copyMissing(ctx, row.ID) {
			entry.Warnings = append(entry.Warnings, "local_file_missing")
		}
		if entry.AttachmentKey == "" && len(pdfs) == 1 {
			entry.AttachmentKey = pdfs[0].Key
		}
		if chosen, _, err := chooseAttachment(attachments, entry.AttachmentKey); err == nil {
			if hash, known := s.sourceHash(ctx, reader, chosen); known {
				entry.IncomingHash = hash
				entry.HashEqual = hash == row.FileHash
			}
		}
	}
	return entry, nil
}

// expandImportItems merges collection expansion and explicit item keys in
// selection order, deduplicating by Zotero key. Collections recurse depth-first
// and a visited set defends against cycles (§5.4).
func expandImportItems(ctx context.Context, reader literature.ZoteroReader, libraryID string, sel literature.ImportSelection) ([]literature.ZoteroItem, []literature.ImportError, error) {
	var items []literature.ZoteroItem
	var itemErrors []literature.ImportError
	seen := make(map[string]struct{})
	appendItem := func(item literature.ZoteroItem) {
		if strings.EqualFold(item.ItemType, "note") || strings.EqualFold(item.ItemType, "attachment") {
			return
		}
		if _, ok := seen[item.Key]; ok {
			return
		}
		seen[item.Key] = struct{}{}
		items = append(items, item)
	}

	if len(sel.CollectionKeys) > 0 {
		collections, err := reader.ListCollections(ctx, libraryID)
		if err != nil {
			return nil, nil, fmt.Errorf("列出 Zotero Collections: %w", err)
		}
		children := make(map[string][]literature.ZoteroCollection, len(collections))
		known := make(map[string]struct{}, len(collections))
		for _, collection := range collections {
			children[collection.ParentKey] = append(children[collection.ParentKey], collection)
			known[collection.Key] = struct{}{}
		}
		visited := make(map[string]struct{})
		var visit func(key string) error
		visit = func(key string) error {
			if _, ok := visited[key]; ok {
				return nil
			}
			visited[key] = struct{}{}
			collectionItems, err := listCollectionItems(ctx, reader, libraryID, key)
			if err != nil {
				return fmt.Errorf("列出 Collection %s 的条目: %w", key, err)
			}
			for _, item := range collectionItems {
				appendItem(item)
			}
			if sel.IncludeSubcollections {
				for _, child := range children[key] {
					if err := visit(child.Key); err != nil {
						return err
					}
				}
			}
			return nil
		}
		for _, key := range sel.CollectionKeys {
			if _, ok := known[key]; !ok {
				return nil, nil, fmt.Errorf("Collection %s 不存在: %w", key, literature.ErrItemNotFound)
			}
			if err := visit(key); err != nil {
				return nil, nil, err
			}
		}
	}

	for _, key := range sel.ItemKeys {
		if key == "" {
			continue
		}
		item, err := reader.GetItem(ctx, libraryID, key)
		if err != nil {
			itemErrors = append(itemErrors, literature.ImportError{
				ZoteroKey: key,
				Message:   fmt.Sprintf("读取 Zotero 条目失败：%v", err),
			})
			continue
		}
		appendItem(item)
	}
	return items, itemErrors, nil
}

// listCollectionItems pages through one collection so large collections are
// never truncated by the reader's page limit.
func listCollectionItems(ctx context.Context, reader literature.ZoteroReader, libraryID, collectionKey string) ([]literature.ZoteroItem, error) {
	var out []literature.ZoteroItem
	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := reader.ListItems(ctx, libraryID, collectionKey, literature.ListItemsOptions{
			Offset: offset,
			Limit:  importPageSize,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if len(page.Items) == 0 {
			break
		}
		offset += len(page.Items)
		if page.Total > 0 && offset >= page.Total {
			break
		}
	}
	return out, nil
}

// importEntry processes one preview entry and returns its outcome. The caller
// aggregates counts and progress.
func (s *Service) importEntry(ctx context.Context, reader literature.ZoteroReader, libraryID string, sel literature.ImportSelection, entry literature.ImportPreviewEntry, actor literature.Actor) literature.ImportOutcome {
	outcome := literature.ImportOutcome{ZoteroKey: entry.ZoteroKey, PaperID: entry.ExistingPaperID}
	item, err := reader.GetItem(ctx, libraryID, entry.ZoteroKey)
	if err != nil {
		return importFailure(outcome, "读取 Zotero 条目失败：%v", err)
	}
	meta := zotero.ToPaperMetadata(item)
	attachments, err := reader.ListAttachments(ctx, libraryID, item.Key)
	if err != nil {
		return importFailure(outcome, "读取 Zotero 附件失败：%v", err)
	}
	chosen, skipStatus, err := chooseAttachment(attachments, entry.AttachmentKey)
	if err != nil {
		return importFailure(outcome, "%v", err)
	}
	if skipStatus != "" {
		outcome.Status = skipStatus
		return outcome
	}
	row, exists, err := s.papers.ByZotero(ctx, libraryID, item.Key)
	if err != nil {
		return importFailure(outcome, "查询已导入文献失败：%v", err)
	}
	if exists {
		outcome.PaperID = row.ID
		hash, hashKnown := s.sourceHash(ctx, reader, chosen)
		return s.finishExisting(ctx, row, meta.Title, hash, hashKnown, sel.ProjectID, actor)
	}
	return s.importNewPaper(ctx, reader, libraryID, item, chosen, meta.Title, sel.ProjectID, actor)
}

// chooseAttachment enforces §5.4: never guess between multiple PDFs, never
// import a non-PDF attachment.
func chooseAttachment(attachments []literature.ZoteroAttachment, choiceKey string) (literature.ZoteroAttachment, literature.ImportStatus, error) {
	if choiceKey != "" {
		for _, attachment := range attachments {
			if attachment.Key != choiceKey {
				continue
			}
			if !isPDFAttachment(attachment) {
				return literature.ZoteroAttachment{}, "", fmt.Errorf("%w: 附件 %s 不是 PDF", literature.ErrInvalidPDF, choiceKey)
			}
			return attachment, "", nil
		}
		return literature.ZoteroAttachment{}, "", fmt.Errorf("Zotero 附件 %s: %w", choiceKey, literature.ErrAttachmentUnresolved)
	}
	pdfs := pdfAttachments(attachments)
	switch len(pdfs) {
	case 0:
		return literature.ZoteroAttachment{}, literature.ImportSkippedNoPDF, nil
	case 1:
		return pdfs[0], "", nil
	default:
		return literature.ZoteroAttachment{}, literature.ImportNeedsAttachment, nil
	}
}

func pdfAttachments(attachments []literature.ZoteroAttachment) []literature.ZoteroAttachment {
	out := make([]literature.ZoteroAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		if isPDFAttachment(attachment) {
			out = append(out, attachment)
		}
	}
	return out
}

// sourceHash hashes a resolvable Zotero original read-only. An unreadable or
// unresolvable source simply yields hashKnown=false; existing papers are still
// associated because the local copy is authoritative (INV-08).
func (s *Service) sourceHash(ctx context.Context, reader literature.ZoteroReader, attachment literature.ZoteroAttachment) (string, bool) {
	resolved, err := reader.ResolveAttachment(ctx, attachment)
	if err != nil {
		return "", false
	}
	hash, err := hashFile(ctx, resolved.Path)
	if err != nil {
		return "", false
	}
	return hash, true
}

// finishExisting associates an already-imported paper. The copy, hash and
// metadata are never touched; a differing source is only warned about.
func (s *Service) finishExisting(ctx context.Context, row papers.Paper, title, sourceHash string, hashKnown bool, projectID string, actor literature.Actor) literature.ImportOutcome {
	outcome := literature.ImportOutcome{ZoteroKey: row.ZoteroKey, PaperID: row.ID}
	if s.copyMissing(ctx, row.ID) {
		outcome.Warnings = append(outcome.Warnings, "local_file_missing")
	}
	if hashKnown && row.FileHash != "" && sourceHash != row.FileHash {
		outcome.Warnings = append(outcome.Warnings, "source_hash_differs")
	}
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return importFailure(outcome, "打开项目失败：%v", err)
	}
	libraryID := row.ZoteroLibraryID
	if libraryID == "" {
		libraryID = "0"
	}
	var added bool
	if err := handle.InTx(ctx, func(tx *sql.Tx) error {
		var linkErr error
		added, linkErr = s.projectPapers(handle).LinkQuiet(ctx, tx, projectpapers.Link{
			ProjectID: projectID,
			PaperID:   row.ID,
			LinkedBy:  events.Actor(actor),
		})
		if linkErr != nil {
			return linkErr
		}
		if !added {
			return nil
		}
		return handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventPaperImported,
			EntityType: schema.EntityPaper,
			EntityID:   row.ID,
			New:        importEventNew(libraryID, row.ZoteroKey, row.FileHash, projectID, true),
			Summary:    importSummary(title),
			Actor:      events.Actor(actor),
		})
	}); err != nil {
		return importFailure(outcome, "写入项目关联失败：%v", err)
	}
	if added {
		outcome.Status = literature.ImportLinkedExisting
	} else {
		outcome.Status = literature.ImportSkippedDuplicate
	}
	return outcome
}

// importNewPaper copies the PDF, inserts the global row and the project
// association, and compensates file + row when a later step fails (§5.11).
func (s *Service) importNewPaper(ctx context.Context, reader literature.ZoteroReader, libraryID string, item literature.ZoteroItem, chosen literature.ZoteroAttachment, title, projectID string, actor literature.Actor) literature.ImportOutcome {
	outcome := literature.ImportOutcome{ZoteroKey: item.Key}
	resolved, err := reader.ResolveAttachment(ctx, chosen)
	if err != nil {
		return importFailure(outcome, "解析 Zotero 附件失败：%v", err)
	}
	if !isPDFAttachment(chosen) && !strings.EqualFold(filepath.Ext(resolved.Path), ".pdf") {
		return importFailure(outcome, "%v", literature.ErrInvalidPDF)
	}
	src, err := os.Open(resolved.Path)
	if err != nil {
		return importFailure(outcome, "打开 PDF 原件失败：%v", err)
	}
	tempID, err := ids.New(s.clock)
	if err != nil {
		_ = src.Close()
		return importFailure(outcome, "生成文献 ID 失败：%v", err)
	}
	stored, err := s.files.Store(ctx, tempID, src, resolved.Size)
	_ = src.Close()
	if err != nil {
		_ = s.files.Remove(context.WithoutCancel(ctx), tempID)
		return importFailure(outcome, "复制 PDF 失败：%v", err)
	}
	metaJSON, err := zotero.MetadataJSON(item)
	if err != nil {
		_ = s.files.Remove(context.WithoutCancel(ctx), tempID)
		return importFailure(outcome, "生成文献元数据失败：%v", err)
	}

	var (
		paperID string
		created bool
		moved   bool
	)
	if err := s.ws.Global().InTx(ctx, func(tx *sql.Tx) error {
		var upsertErr error
		paperID, created, upsertErr = s.papers.Upsert(ctx, tx, papers.Paper{
			ZoteroLibraryID: libraryID,
			ZoteroKey:       item.Key,
			AttachmentKey:   chosen.Key,
			Metadata:        json.RawMessage(metaJSON),
			SourceState:     schema.PaperSourceOK,
		})
		if upsertErr != nil {
			return upsertErr
		}
		if !created {
			return nil
		}
		if paperID != tempID {
			if moveErr := s.files.Move(ctx, tempID, paperID); moveErr != nil {
				return moveErr
			}
			moved = true
		}
		return s.papers.UpdateFile(ctx, tx, paperID,
			s.ws.Paths().PaperPDF(paperID), stored.Hash, stored.Size, schema.PaperFileOK)
	}); err != nil {
		outcome.PaperID = paperID
		s.compensateNewPaper(ctx, tempID, paperID, moved)
		return importFailure(outcome, "写入全局文献库失败：%v", err)
	}

	if !created {
		// A duplicate (concurrent import or repeated key) wins; the temporary
		// copy is discarded and the existing row is only associated.
		_ = s.files.Remove(context.WithoutCancel(ctx), tempID)
		row, err := s.papers.Get(ctx, paperID)
		if err != nil {
			outcome.PaperID = paperID
			return importFailure(outcome, "读取已存在文献失败：%v", err)
		}
		return s.finishExisting(ctx, row, title, stored.Hash, true, projectID, actor)
	}

	outcome.PaperID = paperID
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		s.compensateNewPaper(ctx, tempID, paperID, moved)
		return importFailure(outcome, "打开项目失败：%v", err)
	}
	var alreadyLinked bool
	if err := handle.InTx(ctx, func(tx *sql.Tx) error {
		added, linkErr := s.projectPapers(handle).LinkQuiet(ctx, tx, projectpapers.Link{
			ProjectID: projectID,
			PaperID:   paperID,
			LinkedBy:  events.Actor(actor),
		})
		if linkErr != nil {
			return linkErr
		}
		if !added {
			alreadyLinked = true
			return nil
		}
		return handle.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventPaperImported,
			EntityType: schema.EntityPaper,
			EntityID:   paperID,
			New:        importEventNew(libraryID, item.Key, stored.Hash, projectID, false),
			Summary:    importSummary(title),
			Actor:      events.Actor(actor),
		})
	}); err != nil {
		s.compensateNewPaper(ctx, tempID, paperID, moved)
		return importFailure(outcome, "写入项目关联失败：%v", err)
	}
	if alreadyLinked {
		outcome.Status = literature.ImportSkippedDuplicate
		return outcome
	}
	outcome.Status = literature.ImportCreated
	return outcome
}

// compensateNewPaper removes what this import created: the global row when no
// project still references it, and the copy directories. The copy is kept when
// the row cannot be removed, so the database never points at a missing file.
// The context is detached from cancellation: cleanup must still run when the
// import was canceled (SPEC-04 §5.0).
func (s *Service) compensateNewPaper(ctx context.Context, tempID, paperID string, moved bool) {
	cleanupCtx := context.WithoutCancel(ctx)
	rowGone := true
	if paperID != "" {
		count, err := s.CountPaperRefs(cleanupCtx, paperID, "")
		if err == nil && count > 0 {
			rowGone = false
		} else if err := s.ws.Global().InTx(cleanupCtx, func(tx *sql.Tx) error {
			return s.papers.Delete(cleanupCtx, tx, paperID)
		}); err != nil && !errors.Is(err, sql.ErrNoRows) {
			rowGone = false
		}
	}
	_ = s.files.Remove(cleanupCtx, tempID)
	if moved && paperID != "" && rowGone {
		_ = s.files.Remove(cleanupCtx, paperID)
	}
}

func importEventNew(libraryID, zoteroKey, fileHash, projectID string, reused bool) *events.Object {
	obj := events.Object{
		"zotero_library_id": libraryID,
		"zotero_key":        zoteroKey,
		"file_hash":         fileHash,
		"project_id":        projectID,
		"reused":            reused,
	}
	return &obj
}

func importSummary(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "导入文献"
	}
	return "导入文献《" + truncateRunes(title, importTitleLimit) + "》"
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

func importFailure(outcome literature.ImportOutcome, format string, args ...any) literature.ImportOutcome {
	outcome.Status = literature.ImportFailed
	outcome.Error = fmt.Sprintf(format, args...)
	return outcome
}

func importProgressMessage(outcome literature.ImportOutcome) string {
	switch outcome.Status {
	case literature.ImportCreated:
		return "已导入"
	case literature.ImportLinkedExisting:
		return "已关联到当前项目"
	case literature.ImportSkippedDuplicate:
		return "项目已关联该文献，跳过"
	case literature.ImportSkippedNoPDF:
		return "跳过：没有 PDF 附件"
	case literature.ImportNeedsAttachment:
		return "需要选择 PDF 附件"
	default:
		if outcome.Error != "" {
			return outcome.Error
		}
		return "导入失败"
	}
}

func accumulateImportResult(result *literature.ImportResult, status literature.ImportStatus) {
	switch status {
	case literature.ImportCreated:
		result.Imported++
	case literature.ImportLinkedExisting:
		result.Linked++
	case literature.ImportFailed:
		result.Failed++
	default:
		result.Skipped++
	}
}

// hashFile streams a file through SHA-256 without modifying it.
func hashFile(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hasher := sha256.New()
	buf := make([]byte, copyBufferSize)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			hasher.Write(buf[:n])
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return "", readErr
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
