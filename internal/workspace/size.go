package workspace

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
)

// dirSize sums the sizes of all regular files under dir recursively.
// Symbolic links are not followed and never counted (WalkDir sees them as
// non-regular entries). A missing directory contributes 0 without an error;
// unreadable entries are skipped. The walk stops on context cancellation.
func dirSize(ctx context.Context, dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			return nil // skip unreadable or vanished entries
		}
		if d.Type().IsRegular() {
			if info, ierr := d.Info(); ierr == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}

// dbFilesSize sums a SQLite database file and its -wal/-shm siblings that
// exist. A missing base file contributes 0.
func dbFilesSize(path string) int64 {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total
}

// filePresent reports whether path exists as a non-directory file.
func filePresent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
