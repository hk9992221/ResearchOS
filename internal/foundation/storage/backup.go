package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"researchos/internal/foundation/clock"
)

const (
	// backupKeep is the retention count per database (§4.8.3).
	backupKeep = 5
	// backupTimestampLayout is the backup file name timestamp layout.
	backupTimestampLayout = "20060102-150405"
)

// backup snapshots db with VACUUM INTO, fsyncs the copy and renames it to its
// final name. A backup root that cannot be created is a warning, not a failure
// (§4.8.3), because the migration itself is still transaction protected. A
// failed snapshot or rename aborts the migration (§4.8.2 rule 6).
func backup(ctx context.Context, db *sql.DB, dbPath string, from int, opt Options) (string, error) {
	root := opt.BackupRoot
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			opt.warn("migration backup skipped: user cache dir unavailable", "err", err)
			return "", nil
		}
		root = filepath.Join(cache, "ResearchOS", "backups")
	}
	dir := filepath.Join(root, dbStem(dbPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		opt.warn("migration backup skipped: backup directory unavailable", "dir", dir, "err", err)
		return "", nil
	}

	tmp := filepath.Join(dir, fmt.Sprintf(".tmp-%d-%d.db", os.Getpid(), clock.System{}.Now().UnixNano()))
	_ = os.Remove(tmp)
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return "", fmt.Errorf("storage: migration backup: %w", err)
	}
	if err := syncFile(tmp); err != nil {
		return "", fmt.Errorf("storage: migration backup: fsync: %w", err)
	}

	final := filepath.Join(dir, fmt.Sprintf("%s-v%d.db", opt.Clock.Now().UTC().Format(backupTimestampLayout), from))
	for i := 2; exists(final); i++ {
		final = filepath.Join(dir, fmt.Sprintf("%s-v%d-%d.db", opt.Clock.Now().UTC().Format(backupTimestampLayout), from, i))
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", fmt.Errorf("storage: migration backup: rename: %w", err)
	}
	cleanup = false
	if err := pruneBackups(dir, backupKeep); err != nil {
		opt.warn("migration backup retention failed", "dir", dir, "err", err)
	}
	return final, nil
}

// dbStem is the per-database backup directory name. global.db maps to
// "global"; project.db maps to "project-<project-id>" so that backups from
// different projects never share a retention bucket. (The SPEC writes
// "<db-stem>"; a bare "project" would make retention delete another project's
// snapshots, so the directory name is part of the stem here.)
func dbStem(dbPath string) string {
	base := filepath.Base(dbPath)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "project" {
		if parent := filepath.Base(filepath.Dir(dbPath)); parent != "" && parent != "." && parent != string(filepath.Separator) {
			return "project-" + parent
		}
	}
	return stem
}

// pruneBackups keeps the newest keep snapshot files in dir. Names sort
// chronologically, so a reverse lexical sort is a newest-first order.
func pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".db") {
			continue
		}
		names = append(names, name)
	}
	if len(names) <= keep {
		return nil
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	for _, name := range names[keep:] {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

// syncFile opens name and fsyncs it.
func syncFile(name string) error {
	f, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
