// Package diag runs the read-only self check of §5.14: schema versions,
// integrity, foreign keys, note/file state, broken external references and the
// application configuration. It never repairs anything.
package diag

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"researchos/internal/foundation/config"
	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/storage"
	"researchos/internal/foundation/workspace"
)

// Report is the structured diagnostic result.
type Report struct {
	GeneratedAt time.Time
	Global      DBReport
	Projects    []ProjectReport
	Config      ConfigReport
	Warnings    []string
}

// DBReport describes one database file.
type DBReport struct {
	Path                 string
	SchemaVersion        int
	TargetVersion        int
	IntegrityOK          bool
	IntegrityErr         string
	ForeignKeyViolations []string
	SizeBytes            int64
	WALBytes             int64
}

// ProjectReport describes one project.
type ProjectReport struct {
	ProjectID    string
	DB           *DBReport
	DirExists    bool
	NotesTotal   int
	NotesMissing int
	BrokenRefs   int
}

// Options configures Run.
type Options struct {
	// IncludeFileScan also checks that every active note body file exists.
	IncludeFileScan bool
	// ProjectIDs limits the report; empty means every registered project.
	ProjectIDs []string
	// Migrations is the module migration set registered with the workspace.
	// Without it TargetVersion would only count the baseline schema (§4.8.1).
	Migrations map[storage.Kind][]storage.Migration
}

// ConfigReport describes the application configuration.
type ConfigReport struct {
	Path          string
	Exists        bool
	CorruptBackup string
	WorkspaceRoot string
	RootReachable bool
	ZoteroDataDir string
}

// Run performs the read-only self check. Every failure is reported in Warnings
// (or IntegrityErr) rather than being repaired.
func Run(ctx context.Context, w *workspace.Workspace, opt Options) (Report, error) {
	rep := Report{GeneratedAt: w.Clock().Now()}
	global := w.Global()
	if global == nil {
		return rep, errors.New("diag: workspace has no open global database")
	}

	globalPath := filepath.Join(w.Root(), "global.db")
	globalReport, err := inspectDB(ctx, globalPath, storage.KindGlobal, opt.Migrations)
	rep.Global = globalReport
	if err != nil {
		rep.Warnings = append(rep.Warnings, "global: "+err.Error())
	}

	projectIDs := opt.ProjectIDs
	if len(projectIDs) == 0 {
		registered, err := registeredProjectIDs(ctx, global.DB())
		if err != nil {
			return rep, err
		}
		projectIDs = registered
	}
	projectIDs = sortedUnique(projectIDs)

	for _, projectID := range projectIDs {
		pr, warnings := inspectProject(ctx, w, projectID, opt.IncludeFileScan, opt.Migrations)
		rep.Projects = append(rep.Projects, pr)
		rep.Warnings = append(rep.Warnings, warnings...)
	}

	configReport, warnings := inspectConfig(ctx, w)
	rep.Config = configReport
	rep.Warnings = append(rep.Warnings, warnings...)

	sort.Strings(rep.Warnings)
	return rep, nil
}

func inspectProject(ctx context.Context, w *workspace.Workspace, projectID string, includeFileScan bool, extra map[storage.Kind][]storage.Migration) (ProjectReport, []string) {
	pr := ProjectReport{ProjectID: projectID}
	var warnings []string
	p := w.Paths()

	if dir, err := p.Abs(p.ProjectDir(projectID)); err == nil {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			pr.DirExists = true
		}
	}

	dbPath := filepath.Join(w.Root(), "projects", projectID, "project.db")
	if info, err := os.Stat(dbPath); err != nil || info.IsDir() {
		return pr, warnings
	}
	dbReport, err := inspectDB(ctx, dbPath, storage.KindProject, extra)
	pr.DB = &dbReport
	if err != nil {
		warnings = append(warnings, projectID+": "+err.Error())
		return pr, warnings
	}

	db, err := storage.OpenReadOnly(ctx, dbPath)
	if err != nil {
		warnings = append(warnings, projectID+": "+err.Error())
		return pr, warnings
	}
	defer db.Close()

	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM notes WHERE deleted_at IS NULL`).Scan(&pr.NotesTotal); err != nil {
		warnings = append(warnings, projectID+": count notes: "+err.Error())
	}
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM notes WHERE deleted_at IS NULL AND file_state = 'missing'`).Scan(&pr.NotesMissing); err != nil {
		warnings = append(warnings, projectID+": count missing notes: "+err.Error())
	}
	if includeFileScan {
		rows, err := db.QueryContext(ctx,
			`SELECT file_path FROM notes WHERE deleted_at IS NULL AND file_state <> 'missing'`)
		if err != nil {
			warnings = append(warnings, projectID+": scan note files: "+err.Error())
		} else {
			for rows.Next() {
				var rel string
				if err := rows.Scan(&rel); err != nil {
					break
				}
				abs, err := p.Abs(rel)
				if err != nil {
					pr.NotesMissing++
					continue
				}
				if _, err := os.Stat(abs); err != nil {
					pr.NotesMissing++
				}
			}
			rows.Close()
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM external_refs WHERE status = 'broken'`).Scan(&pr.BrokenRefs); err != nil {
		warnings = append(warnings, projectID+": count broken refs: "+err.Error())
	}
	return pr, warnings
}

func inspectDB(ctx context.Context, path string, kind storage.Kind, extra map[storage.Kind][]storage.Migration) (DBReport, error) {
	rep := DBReport{Path: path}
	if info, err := os.Stat(path); err == nil {
		rep.SizeBytes = info.Size()
	}
	if info, err := os.Stat(path + "-wal"); err == nil {
		rep.WALBytes = info.Size()
	}
	rep.TargetVersion, _ = storage.TargetVersion(kind, storage.Options{Extra: extra})

	db, err := storage.OpenReadOnly(ctx, path)
	if err != nil {
		rep.IntegrityErr = err.Error()
		return rep, err
	}
	defer db.Close()

	var hasMigrations int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&hasMigrations); err != nil {
		return rep, fmt.Errorf("inspect schema_migrations: %w", err)
	}
	if hasMigrations > 0 {
		if err := db.QueryRowContext(ctx, `SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&rep.SchemaVersion); err != nil {
			return rep, fmt.Errorf("read schema version: %w", err)
		}
	}

	problems, err := pragmaProblems(ctx, db, `PRAGMA integrity_check`, false)
	if err != nil {
		rep.IntegrityErr = err.Error()
	} else if len(problems) == 0 {
		rep.IntegrityOK = true
	} else {
		rep.IntegrityErr = strings.Join(problems, "; ")
	}

	foreignKeys, err := pragmaProblems(ctx, db, `PRAGMA foreign_key_check`, true)
	if err != nil {
		return rep, fmt.Errorf("foreign_key_check: %w", err)
	}
	rep.ForeignKeyViolations = foreignKeys
	return rep, nil
}

// pragmaProblems returns the rows of a check pragma. integrity_check reports the
// literal "ok"; foreign_key_check reports one row per violation.
func pragmaProblems(ctx context.Context, db *sql.DB, query string, allRows bool) ([]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []string
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		if !allRows {
			if s, ok := values[0].(string); ok && s == "ok" {
				continue
			}
			if s, ok := values[0].(string); ok {
				out = append(out, s)
				continue
			}
		}
		parts := make([]string, len(values))
		for i, v := range values {
			parts[i] = fmt.Sprint(v)
		}
		out = append(out, strings.Join(parts, "/"))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func inspectConfig(ctx context.Context, w *workspace.Workspace) (ConfigReport, []string) {
	var warnings []string
	store := w.Config()
	if store == nil {
		var err error
		store, err = config.NewStore()
		if err != nil {
			return ConfigReport{}, []string{"config: " + err.Error()}
		}
	}
	rep := ConfigReport{Path: store.Path()}
	if _, err := os.Stat(rep.Path); err == nil {
		rep.Exists = true
	}
	cfg, err := store.Load(ctx)
	if err != nil {
		if errors.Is(err, errs.ErrConfigCorrupt) {
			if matches, globErr := filepath.Glob(rep.Path + ".corrupt-*"); globErr == nil && len(matches) > 0 {
				sort.Strings(matches)
				rep.CorruptBackup = matches[len(matches)-1]
			}
		} else {
			warnings = append(warnings, "config: "+err.Error())
		}
	}
	rep.WorkspaceRoot = cfg.WorkspaceRoot
	rep.ZoteroDataDir = cfg.ZoteroDataDir
	if info, err := os.Stat(w.Root()); err == nil && info.IsDir() {
		rep.RootReachable = true
	}
	return rep, warnings
}

func registeredProjectIDs(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("diag: list projects: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("diag: list projects: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("diag: list projects: %w", err)
	}
	return out, nil
}

func sortedUnique(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
