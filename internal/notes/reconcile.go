package notes

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"researchos/internal/foundation/clock"
	"researchos/internal/foundation/notefile"
	"researchos/internal/foundation/reconcile"
	"researchos/internal/foundation/schema"
	"researchos/internal/foundation/workspace"
)

// reconcileService wraps the foundation file/DB reconciliation with the
// SPEC-05 issue and decision shapes (SPEC-05 §5.8). It is manual-only and never
// deletes a file or a row.
type reconcileService struct {
	ws        *workspace.Workspace
	projectID string
	clock     clock.Clock
}

var _ ReconcileService = (*reconcileService)(nil)

// NewReconcileService binds the reconciliation service to one project of ws.
func NewReconcileService(ws *workspace.Workspace, projectID string, opt ServiceOptions) *reconcileService {
	if opt.Clock == nil {
		opt.Clock = clock.System{}
	}
	return &reconcileService{ws: ws, projectID: projectID, clock: opt.Clock}
}

// Scan reports orphan files, missing files, externally modified bodies and
// unreferenced attachments. Missing bodies are additionally flagged
// file_state='missing' so the editor turns read-only (acceptance §8 item 5);
// that flag is a storage fact, not a content change, so no event is emitted.
func (s *reconcileService) Scan(ctx context.Context, projectID string) (*ReconcileReport, error) {
	if projectID == "" {
		projectID = s.projectID
	}
	if projectID != s.projectID {
		return nil, fmt.Errorf("notes: reconcile project %q does not match the service project %q", projectID, s.projectID)
	}
	rep, err := reconcile.Scan(ctx, s.ws, projectID)
	if err != nil {
		return nil, err
	}
	handle, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	issues := make([]ReconcileIssue, 0, len(rep.Orphans)+len(rep.Missing)+len(rep.Modified))
	for _, orphan := range rep.Orphans {
		issues = append(issues, ReconcileIssue{
			Kind:   IssueOrphanFile,
			Path:   orphan.RelPath,
			Detail: fmt.Sprintf("磁盘文件未登记（%d 字节）", orphan.Size),
		})
	}
	for _, missing := range rep.Missing {
		if err := noteMarkFileState(ctx, handle.DB(), missing.NoteID, BodyMissing); err != nil {
			return nil, err
		}
		issues = append(issues, ReconcileIssue{
			Kind:   IssueMissingFile,
			NoteID: missing.NoteID,
			Path:   missing.FilePath,
			Detail: "正文文件缺失：" + missing.Title,
		})
	}
	for _, modified := range rep.Modified {
		issues = append(issues, ReconcileIssue{
			Kind:   IssueExternalChange,
			NoteID: modified.NoteID,
			Path:   modified.FilePath,
			Detail: fmt.Sprintf("正文与基线不一致（db=%s disk=%s）", modified.DBHash, modified.DiskHash),
		})
	}
	attachments, err := s.attachmentIssues(ctx, handle, projectID)
	if err != nil {
		return nil, err
	}
	issues = append(issues, attachments...)
	sortIssues(issues)
	return &ReconcileReport{Issues: issues, ScannedAt: s.clock.Now()}, nil
}

// Apply executes each decision and returns the issues that remain. Every
// decision commits on its own (foundation reconcile.Apply); a failing decision
// never blocks the rest, and its error is returned alongside the report.
func (s *reconcileService) Apply(ctx context.Context, decisions []ReconcileDecision) (*ReconcileReport, error) {
	actions := make([]reconcile.Action, 0, len(decisions))
	for _, decision := range decisions {
		action, use, err := s.decisionAction(ctx, decision)
		if err != nil {
			return nil, err
		}
		if use {
			actions = append(actions, action)
		}
	}
	res, err := reconcile.Apply(ctx, s.ws, s.projectID, actions)
	if err != nil {
		return nil, err
	}
	var failures []error
	for _, item := range res.Items {
		if item.Err != nil {
			failures = append(failures, fmt.Errorf(
				"notes: reconcile %s (note %s): %w", item.Action.Kind, item.NoteID, item.Err))
		}
	}
	report, scanErr := s.Scan(ctx, s.projectID)
	if scanErr != nil {
		return nil, errors.Join(append(failures, scanErr)...)
	}
	return report, errors.Join(failures...)
}

// decisionAction maps one SPEC-05 decision to a foundation action.
func (s *reconcileService) decisionAction(ctx context.Context, decision ReconcileDecision) (reconcile.Action, bool, error) {
	actor := noteActor(decision.Actor)
	switch decision.Action {
	case ActionRegisterOrphan:
		title := strings.TrimSpace(decision.Title)
		if title == "" {
			title = s.orphanTitle(decision.Path)
		}
		return reconcile.Action{
			Kind:    reconcile.ActionAdopt,
			RelPath: decision.Path,
			AdoptOptions: reconcile.AdoptOptions{
				Title: title,
				// SPEC-05 §5.8: an adopted orphan becomes a scratchpad note.
				Kind:  schema.NoteScratchpad,
				Actor: actor,
			},
		}, true, nil
	case ActionCreateEmpty:
		return reconcile.Action{
			Kind:         reconcile.ActionRecreate,
			NoteID:       decision.NoteID,
			AdoptOptions: reconcile.AdoptOptions{Actor: actor},
		}, true, nil
	case ActionAdoptExternal:
		return reconcile.Action{
			Kind:         reconcile.ActionAcceptExternalEdit,
			NoteID:       decision.NoteID,
			AdoptOptions: reconcile.AdoptOptions{Actor: actor},
		}, true, nil
	case ActionIgnore, "":
		return reconcile.Action{}, false, nil
	default:
		return reconcile.Action{}, false, fmt.Errorf("notes: unknown reconcile action %q", decision.Action)
	}
}

// orphanTitle derives the registration title from the file: its frontmatter
// title, else the first non-empty body line (SPEC-05 §5.8), else the file name
// without its extension.
func (s *reconcileService) orphanTitle(relPath string) string {
	if relPath == "" {
		return ""
	}
	abs, err := s.ws.Paths().Abs(relPath)
	if err != nil {
		return ""
	}
	if body, err := os.ReadFile(abs); err == nil {
		meta, markdown := notefile.Split(body)
		if title := strings.TrimSpace(meta.Title); title != "" {
			return title
		}
		if title := noteFirstLine(markdown); title != "" {
			return title
		}
	}
	return strings.TrimSuffix(filepath.Base(relPath), filepath.Ext(relPath))
}

// attachmentIssues reports files under attachments/<note-id>/ that the note
// body does not reference. Nothing is deleted (SPEC-05 §10 O-09). A note whose
// body cannot be read is skipped: without the body the reference test would
// produce noise.
func (s *reconcileService) attachmentIssues(ctx context.Context, handle *workspace.ProjectHandle, projectID string) ([]ReconcileIssue, error) {
	rows, err := handle.DB().QueryContext(ctx, `SELECT id FROM notes`)
	if err != nil {
		return nil, fmt.Errorf("notes: scan attachments: %w", err)
	}
	var noteIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("notes: scan attachments: %w", err)
		}
		noteIDs = append(noteIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("notes: scan attachments: %w", err)
	}
	rows.Close()

	p := s.ws.Paths()
	var issues []ReconcileIssue
	for _, noteID := range noteIDs {
		var filePath string
		if err := handle.DB().QueryRowContext(ctx, `SELECT file_path FROM notes WHERE id = ?`, noteID).
			Scan(&filePath); err != nil {
			return nil, fmt.Errorf("notes: scan attachments: %w", err)
		}
		noteAbs, err := p.Abs(filePath)
		if err != nil {
			return nil, err
		}
		body, err := os.ReadFile(noteAbs)
		if err != nil {
			continue
		}
		dirAbs, err := p.Abs(p.AttachmentsDir(projectID, noteID))
		if err != nil {
			return nil, err
		}
		walkErr := filepath.WalkDir(dirAbs, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			name, err := filepath.Rel(dirAbs, path)
			if err != nil {
				return err
			}
			markdownPath := "../attachments/" + noteID + "/" + filepath.ToSlash(name)
			if strings.Contains(string(body), markdownPath) {
				return nil
			}
			rel, err := p.Rel(path)
			if err != nil {
				return err
			}
			issues = append(issues, ReconcileIssue{
				Kind:   IssueOrphanAttachment,
				NoteID: noteID,
				Path:   rel,
				Detail: "正文未引用该附件（仅报告，不删除）：" + markdownPath,
			})
			return nil
		})
		if walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist) {
			return nil, fmt.Errorf("notes: scan attachments %s: %w", noteID, walkErr)
		}
	}
	return issues, nil
}

// sortIssues gives the report a deterministic order.
func sortIssues(issues []ReconcileIssue) {
	sort.Slice(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.NoteID != b.NoteID {
			return a.NoteID < b.NoteID
		}
		return a.Path < b.Path
	})
}
