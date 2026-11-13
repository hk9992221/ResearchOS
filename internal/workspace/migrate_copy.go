package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// copyBufferSize is the streaming chunk of §5.10: files are copied and hashed
// 1 MiB at a time, never read into memory whole.
const copyBufferSize = 1 << 20

// manifestEntry records one copied regular file. Hash is empty when the
// verification mode does not hash that file.
type manifestEntry struct {
	Rel  string // slash-separated path relative to the workspace root
	Size int64
	Hash string // lowercase hex SHA256, or ""
}

// migrationManifest is the proof of what the copy wrote. It is built while
// copying (the source is read exactly once) and compared against the staged
// destination during verification.
type migrationManifest struct {
	Entries []manifestEntry
	Files   int
	Bytes   int64
}

// dirMeta remembers a directory's mode and mtime, applied after its children
// exist so a read-only source directory can still be populated.
type dirMeta struct {
	path  string
	mode  fs.FileMode
	mtime time.Time
}

// scanMigrationSource counts what the copy will transfer. It applies the same
// skip rules as copyTree: exports/ unless requested, symlinks, and non-regular
// files.
func scanMigrationSource(ctx context.Context, root string, includeExports bool) (int, int64, error) {
	exports := filepath.Join(root, "exports")
	sep := string(filepath.Separator)
	var (
		files int
		bytes int64
	)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return fmt.Errorf("workspace: migrate: scan %s: %w", path, walkErr)
		}
		if path == root {
			return nil
		}
		if !includeExports && (path == exports || strings.HasPrefix(path, exports+sep)) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			// Symlinks are skipped during the copy (§5.10): the target would
			// otherwise depend on a path outside the workspace or break on
			// another volume. They are documented as not migrated.
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("workspace: migrate: stat %s: %w", path, err)
		}
		files++
		bytes += info.Size()
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return files, bytes, nil
}

// copyTree copies the source workspace into the staging directory, hashing
// while writing. It preserves mtime and mode and keeps the workspace-relative
// layout so internal paths stay valid (§5.10 step 4).
func (s *Service) copyTree(job *migrateJob, plan *MigratePlan) (*migrationManifest, error) {
	src := plan.SourceRoot
	dst := plan.StagingRoot
	exports := filepath.Join(src, "exports")
	sep := string(filepath.Separator)
	buf := make([]byte, copyBufferSize)
	manifest := &migrationManifest{}
	var dirs []dirMeta

	needHash := func(rel string) bool {
		if plan.Options.Verify == VerifyFull {
			return true
		}
		ext := strings.ToLower(filepath.Ext(rel))
		return ext == ".db" || ext == ".md"
	}

	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if err := job.ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return fmt.Errorf("workspace: migrate: walk %s: %w", path, walkErr)
		}
		if path == src {
			return nil
		}
		if !plan.Options.IncludeExports && (path == exports || strings.HasPrefix(path, exports+sep)) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("workspace: migrate: stat %s: %w", path, err)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return fmt.Errorf("workspace: migrate: relative path %s: %w", path, err)
		}
		target := filepath.Join(dst, rel)

		switch {
		case info.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("workspace: migrate: create %s: %w", target, err)
			}
			dirs = append(dirs, dirMeta{path: target, mode: info.Mode().Perm(), mtime: info.ModTime()})
		case info.Mode().IsRegular():
			digest, written, err := copyMigrationFile(job.ctx, buf, path, target, info, needHash(rel))
			if err != nil {
				return err
			}
			manifest.Entries = append(manifest.Entries, manifestEntry{
				Rel:  filepath.ToSlash(rel),
				Size: written,
				Hash: digest,
			})
			manifest.Files++
			manifest.Bytes += written
			job.publish(MigrateProgress{
				Phase:      phaseCopy,
				FilesDone:  manifest.Files,
				FilesTotal: plan.Files,
				BytesDone:  manifest.Bytes,
				BytesTotal: plan.Bytes,
				Current:    filepath.ToSlash(rel),
			})
		default:
			// Devices, sockets and pipes are not workspace data.
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Directories are shrunk to their final mode after their contents exist.
	for _, dir := range dirs {
		if err := os.Chmod(dir.path, dir.mode); err != nil {
			return nil, fmt.Errorf("workspace: migrate: chmod %s: %w", dir.path, err)
		}
		// Best-effort: some file systems do not store directory times.
		_ = os.Chtimes(dir.path, dir.mtime, dir.mtime)
	}
	return manifest, nil
}

// copyMigrationFile streams one file with 1 MiB reads, checking the context
// before every chunk. It returns the SHA256 when hashing was requested and the
// number of bytes written.
func copyMigrationFile(ctx context.Context, buf []byte, src, dst string, info fs.FileInfo, hashIt bool) (string, int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", 0, fmt.Errorf("workspace: migrate: open %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return "", 0, fmt.Errorf("workspace: migrate: create %s: %w", dst, err)
	}

	var (
		hasher  hash.Hash
		writer  io.Writer = out
		written int64
	)
	if hashIt {
		hasher = sha256.New()
		writer = io.MultiWriter(out, hasher)
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = out.Close()
			return "", 0, err
		}
		n, readErr := in.Read(buf)
		if n > 0 {
			if _, writeErr := writer.Write(buf[:n]); writeErr != nil {
				_ = out.Close()
				return "", 0, fmt.Errorf("workspace: migrate: write %s: %w", dst, writeErr)
			}
			written += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = out.Close()
			return "", 0, fmt.Errorf("workspace: migrate: read %s: %w", src, readErr)
		}
	}
	if err := out.Close(); err != nil {
		return "", 0, fmt.Errorf("workspace: migrate: close %s: %w", dst, err)
	}
	// Umask may have narrowed the creation mode; set the source mode exactly.
	if err := os.Chmod(dst, info.Mode().Perm()); err != nil {
		return "", 0, fmt.Errorf("workspace: migrate: chmod %s: %w", dst, err)
	}
	if err := os.Chtimes(dst, info.ModTime(), info.ModTime()); err != nil {
		return "", 0, fmt.Errorf("workspace: migrate: set times %s: %w", dst, err)
	}
	if hasher != nil {
		return hex.EncodeToString(hasher.Sum(nil)), written, nil
	}
	return "", written, nil
}

// verifyMigration re-reads only the destination and compares it with the
// manifest built during the copy: file count, total bytes and the required
// SHA256 set. Verification never touches the source (§5.10 step 5).
func verifyMigration(job *migrateJob, plan *MigratePlan, manifest *migrationManifest) error {
	staging := plan.StagingRoot
	byRel := make(map[string]manifestEntry, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		byRel[entry.Rel] = entry
	}
	buf := make([]byte, copyBufferSize)
	seen := make(map[string]struct{}, len(manifest.Entries))
	var (
		files int
		bytes int64
	)

	err := filepath.WalkDir(staging, func(path string, d fs.DirEntry, walkErr error) error {
		if err := job.ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return fmt.Errorf("workspace: migrate: verify walk %s: %w", path, walkErr)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("workspace: migrate: verify: unexpected non-regular entry %s", path)
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("workspace: migrate: verify stat %s: %w", path, err)
		}
		rel, err := filepath.Rel(staging, path)
		if err != nil {
			return fmt.Errorf("workspace: migrate: verify relative path %s: %w", path, err)
		}
		relSlash := filepath.ToSlash(rel)
		seen[relSlash] = struct{}{}
		files++
		bytes += info.Size()

		entry, ok := byRel[relSlash]
		if !ok {
			return fmt.Errorf("workspace: migrate: verify: %s was not in the copy manifest", relSlash)
		}
		if info.Size() != entry.Size {
			return fmt.Errorf("workspace: migrate: verify: %s size %d, want %d", relSlash, info.Size(), entry.Size)
		}
		if entry.Hash != "" {
			digest, err := hashMigrationFile(job.ctx, buf, path)
			if err != nil {
				return err
			}
			if digest != entry.Hash {
				return fmt.Errorf("workspace: migrate: verify: %s sha256 mismatch", relSlash)
			}
		}
		job.publish(MigrateProgress{
			Phase:      phaseVerify,
			FilesDone:  files,
			FilesTotal: plan.Files,
			BytesDone:  bytes,
			BytesTotal: plan.Bytes,
			Current:    relSlash,
		})
		return nil
	})
	if err != nil {
		return err
	}
	if files != manifest.Files {
		return fmt.Errorf("workspace: migrate: verify: %d files copied, manifest has %d", files, manifest.Files)
	}
	if bytes != manifest.Bytes {
		return fmt.Errorf("workspace: migrate: verify: %d bytes copied, manifest has %d", bytes, manifest.Bytes)
	}
	if len(seen) != len(manifest.Entries) {
		return fmt.Errorf("workspace: migrate: verify: %d entries seen, manifest has %d", len(seen), len(manifest.Entries))
	}
	return nil
}

// hashMigrationFile hashes one file with 1 MiB reads and a context check per
// chunk.
func hashMigrationFile(ctx context.Context, buf []byte, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("workspace: migrate: verify open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			if _, err := h.Write(buf[:n]); err != nil {
				return "", fmt.Errorf("workspace: migrate: verify hash %s: %w", path, err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("workspace: migrate: verify read %s: %w", path, readErr)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
