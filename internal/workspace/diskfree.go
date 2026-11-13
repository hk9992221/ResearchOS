package workspace

import (
	"os"
	"path/filepath"
)

// DiskFree reports the bytes available to the calling user and the total bytes
// of the volume holding path (SPEC-03 §5.10).
//
// path may not exist yet: the probe walks up to the nearest existing ancestor
// so callers can reserve space in a directory they are about to create. ok is
// false when the platform cannot answer or no ancestor exists.
func DiskFree(path string) (free, total int64, ok bool) {
	if path == "" {
		return 0, 0, false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, 0, false
	}
	for {
		if _, err := os.Stat(abs); err == nil {
			break
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return 0, 0, false
		}
		abs = parent
	}
	return diskSpace(abs)
}
