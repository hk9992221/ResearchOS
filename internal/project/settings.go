package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"researchos/internal/export"
	"researchos/internal/foundation/settings"
	"researchos/internal/foundation/trash"
	"researchos/internal/foundation/workspace"
	"researchos/internal/ports"
)

const (
	exportDirKey    = "export.dir"
	exportFormatKey = "export.format"
)

// SettingsView is the Settings page projection (§5.5).
type SettingsView struct {
	Project          Project
	DefaultExportDir string
	DefaultFormat    export.Format
	LastExportedAt   *time.Time
	Storage          ProjectStorage
	Trash            []ports.TrashItem
}

// SettingsPatch edits project-level export defaults. A nil pointer leaves the
// field unchanged; an empty DefaultExportDir restores the workspace default.
type SettingsPatch struct {
	DefaultExportDir *string
	DefaultFormat    *export.Format
}

// ProjectStorage is the storage summary of the Settings page.
type ProjectStorage struct {
	DirBytes        int64
	NoteCount       int
	AttachmentCount int
	PaperCount      int
	PaperBytes      int64
}

// Settings reads export defaults, export history, storage usage and the trash.
// Archived projects are readable.
func (s *Service) Settings(ctx context.Context, projectID string) (*SettingsView, error) {
	p, err := s.Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if s.ws == nil {
		return nil, errNoWorkspace
	}
	h, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("project: settings %s: %w", projectID, err)
	}
	db := h.DB()
	store := settings.NewProject(db).WithClock(s.clock)

	view := &SettingsView{Project: *p}
	view.DefaultExportDir, err = s.defaultExportDir(ctx, store)
	if err != nil {
		return nil, err
	}
	view.DefaultFormat, err = defaultExportFormat(ctx, store)
	if err != nil {
		return nil, err
	}
	view.LastExportedAt, err = lastExportedAt(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("project: settings %s: export history: %w", projectID, err)
	}
	if view.Storage, err = s.projectStorage(ctx, projectID, db); err != nil {
		return nil, err
	}
	if view.Trash, err = s.trashItems(ctx, projectID, h, db); err != nil {
		return nil, err
	}
	return view, nil
}

// UpdateSettings writes project-level export defaults inside one project
// transaction. It obeys the §5.8 read-only guard and writes no Activity.
func (s *Service) UpdateSettings(ctx context.Context, projectID string, patch SettingsPatch) error {
	if err := s.assertWritable(ctx, projectID); err != nil {
		return err
	}
	if patch.DefaultFormat != nil && !validExportFormat(*patch.DefaultFormat) {
		return &InvalidFieldError{
			Field:  exportFormatKey,
			Reason: fmt.Sprintf("must be folder, zip or tar.gz, got %q", string(*patch.DefaultFormat)),
			Err:    ErrInvalidInput,
		}
	}
	if patch.DefaultExportDir != nil && *patch.DefaultExportDir != "" && !filepath.IsAbs(*patch.DefaultExportDir) {
		return &InvalidFieldError{
			Field:  exportDirKey,
			Reason: "must be an absolute path (or the empty string to reset)",
			Err:    ErrInvalidInput,
		}
	}
	if s.ws == nil {
		return errNoWorkspace
	}
	h, err := s.ws.Project(ctx, projectID)
	if err != nil {
		return fmt.Errorf("project: update settings %s: %w", projectID, err)
	}
	store := settings.NewProject(h.DB()).WithClock(s.clock)
	return h.InTx(ctx, func(tx *sql.Tx) error {
		if patch.DefaultExportDir != nil {
			if *patch.DefaultExportDir == "" {
				if err := store.Delete(ctx, tx, exportDirKey); err != nil {
					return err
				}
			} else if err := store.Set(ctx, tx, exportDirKey, *patch.DefaultExportDir); err != nil {
				return err
			}
		}
		if patch.DefaultFormat != nil {
			if err := store.Set(ctx, tx, exportFormatKey, string(*patch.DefaultFormat)); err != nil {
				return err
			}
		}
		return nil
	})
}

// defaultExportDir resolves project value → workspace value → <root>/exports.
func (s *Service) defaultExportDir(ctx context.Context, project *settings.Store) (string, error) {
	value, ok, err := project.Get(ctx, exportDirKey)
	if err != nil {
		return "", err
	}
	if ok && value != "" {
		return value, nil
	}
	if s.settings != nil {
		wsValue, wsOK, err := s.settings.Get(ctx, exportDirKey)
		if err != nil {
			return "", err
		}
		if wsOK && wsValue != "" {
			return wsValue, nil
		}
	}
	resolver := s.ws.Paths()
	return filepath.Join(s.ws.Root(), filepath.FromSlash(resolver.ExportsDir())), nil
}

// defaultExportFormat reads export.format; a missing or invalid stored value
// falls back to zip.
func defaultExportFormat(ctx context.Context, project *settings.Store) (export.Format, error) {
	raw, ok, err := project.Get(ctx, exportFormatKey)
	if err != nil {
		return export.FormatZip, err
	}
	if ok {
		if f := export.Format(raw); validExportFormat(f) {
			return f, nil
		}
	}
	return export.FormatZip, nil
}

func validExportFormat(f export.Format) bool {
	switch f {
	case export.FormatFolder, export.FormatZip, export.FormatTarGz:
		return true
	default:
		return false
	}
}

// projectStorage aggregates directory bytes and row/file counts for the
// Settings storage block.
func (s *Service) projectStorage(ctx context.Context, projectID string, db *sql.DB) (ProjectStorage, error) {
	var st ProjectStorage
	var err error
	if st.DirBytes, err = s.dirBytes(ctx, projectID); err != nil {
		return st, err
	}
	if st.NoteCount, err = countRows(ctx, db, `SELECT count(*) FROM notes WHERE deleted_at IS NULL`); err != nil {
		return st, err
	}
	if st.AttachmentCount, err = s.attachmentCount(projectID); err != nil {
		return st, err
	}
	paperIDs, err := paperIDsOfProject(ctx, db)
	if err != nil {
		return st, err
	}
	if len(paperIDs) == 0 {
		return st, nil
	}
	if s.ws == nil {
		return st, errNoWorkspace
	}
	global, err := s.globalHandle()
	if err != nil {
		return st, err
	}
	query := `SELECT count(*), coalesce(sum(file_size), 0) FROM papers WHERE id IN (` +
		placeholders(len(paperIDs)) + `)`
	args := make([]any, len(paperIDs))
	for i, id := range paperIDs {
		args[i] = id
	}
	if err := global.DB().QueryRowContext(ctx, query, args...).Scan(&st.PaperCount, &st.PaperBytes); err != nil {
		return st, fmt.Errorf("project: settings %s: paper storage: %w", projectID, err)
	}
	return st, nil
}

// attachmentCount counts the regular files under projects/<id>/attachments.
func (s *Service) attachmentCount(projectID string) (int, error) {
	if s.ws == nil {
		return 0, errNoWorkspace
	}
	resolver := s.ws.Paths()
	rel := resolver.ProjectDir(projectID) + "/attachments"
	dir, err := resolver.Abs(rel)
	if err != nil {
		return 0, err
	}
	count := 0
	err = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("project: settings %s: attachments: %w", projectID, err)
	}
	return count, nil
}

// paperIDsOfProject returns the paper ids linked to the project.
func paperIDsOfProject(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT paper_id FROM project_papers ORDER BY paper_id`)
	if err != nil {
		return nil, fmt.Errorf("project: project_papers: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// trashItems lists the recycle bin through the port when present, otherwise
// through SPEC-01's read-only trash query.
func (s *Service) trashItems(ctx context.Context, projectID string, h *workspace.ProjectHandle, db *sql.DB) ([]ports.TrashItem, error) {
	if s.trash != nil {
		return s.trash.List(ctx, projectID)
	}
	svc := trash.New(projectID, db, s.ws.Paths(), h.Events(), nil, s.clock)
	items, err := svc.List(ctx, trash.ListQuery{Limit: 500})
	if err != nil {
		return nil, fmt.Errorf("project: settings %s: trash: %w", projectID, err)
	}
	out := make([]ports.TrashItem, 0, len(items))
	for _, item := range items {
		out = append(out, ports.TrashItem{
			EntityType: string(item.EntityType),
			EntityID:   item.ID,
			Title:      item.Title,
			DeletedAt:  item.DeletedAt,
		})
	}
	return out, nil
}

// placeholders builds "?,?,?" for an IN clause.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
