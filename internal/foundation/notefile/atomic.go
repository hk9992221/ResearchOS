// Package notefile owns the note Markdown body files (SPEC-01 §5.10). The file
// is the authority for the body; project.db stores the metadata. Writes are
// atomic: same-directory temp file, fsync, rename.
package notefile

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"researchos/internal/foundation/paths"
)

// FileStat describes a note body file.
type FileStat struct {
	Hash    string
	ModTime time.Time
	Size    int64
}

// renameFile is a test seam: it must stay os.Rename in production. A test
// replaces it to prove that a failure before the rename leaves the target
// untouched.
var renameFile = os.Rename

// WriteAtomic writes data to abs through a same-directory temp file. The target
// is never opened for writing or truncated: it is replaced by the final rename,
// so a crash before the rename leaves the old content intact and only a temp
// file behind. Parent directories are created with 0755.
func WriteAtomic(abs string, data []byte) (FileStat, error) {
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, paths.DirMode); err != nil {
		return FileStat{}, fmt.Errorf("notefile: create notes directory: %w", err)
	}
	suffix, err := randomSuffix()
	if err != nil {
		return FileStat{}, err
	}
	tmp := filepath.Join(dir, "."+filepath.Base(abs)+".tmp-"+suffix)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, paths.FileMode)
	if err != nil {
		return FileStat{}, fmt.Errorf("notefile: create temp file: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		f.Close()
		return FileStat{}, fmt.Errorf("notefile: write temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return FileStat{}, fmt.Errorf("notefile: fsync temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return FileStat{}, fmt.Errorf("notefile: close temp file: %w", err)
	}
	if err := renameFile(tmp, abs); err != nil {
		return FileStat{}, fmt.Errorf("notefile: rename into place: %w", err)
	}
	cleanup = false
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	info, err := os.Stat(abs)
	if err != nil {
		return FileStat{}, fmt.Errorf("notefile: stat written file: %w", err)
	}
	return FileStat{Hash: hashBytes(data), ModTime: info.ModTime().UTC(), Size: info.Size()}, nil
}

// HashBytes returns the lowercase hex sha256 used by notes.file_hash.
func HashBytes(data []byte) string { return hashBytes(data) }

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func randomSuffix() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("notefile: random suffix: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
