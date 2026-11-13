package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	fpaths "researchos/internal/foundation/paths"
)

// chunkSize is the streaming unit. Cancellation is honoured between chunks, so
// a 1 MiB chunk keeps the worst-case cancel latency well below the §5.9 200 ms
// bound for local disks.
const chunkSize = 1 << 20

// bufferPool reuses the 1 MiB copy buffers; the exporter never allocates a
// buffer per file.
var bufferPool = sync.Pool{New: func() any { b := make([]byte, chunkSize); return &b }}

func getBuffer() []byte  { return *(bufferPool.Get().(*[]byte)) }
func putBuffer(b []byte) { bufferPool.Put(&b) }

// bundleSink writes the bundle entries of one format. bundlePath is always
// bundle-relative with '/'. addFile returns the SHA-256 of the copied bytes,
// computed in the same pass that writes them (§4.5).
type bundleSink interface {
	addFile(ctx context.Context, bundlePath, srcPath string, kind FileKind) (sha string, n int64, err error)
	addBytes(ctx context.Context, bundlePath string, data []byte) (sha string, n int64, err error)
	finish() error
}

// directPlacer is implemented by sinks that can host an entry at its final
// bundle path before the container is published. Only the folder format can:
// it lets the SQLite snapshot land directly, so the database is never copied
// twice.
type directPlacer interface {
	placePath(bundlePath string) (string, error)
}

// newSink chooses the writer and returns it with the temporary .part path
// (empty for the folder format).
func newSink(format Format, staging, destDir, base string) (bundleSink, string, error) {
	switch format {
	case FormatFolder:
		return &folderSink{root: staging}, "", nil
	case FormatZip:
		part := filepath.Join(destDir, "."+base+format.extension()+".part")
		s, err := newZipSink(part, base+"/")
		return s, part, err
	case FormatTarGz:
		part := filepath.Join(destDir, "."+base+format.extension()+".part")
		s, err := newTarGzSink(part, base+"/")
		return s, part, err
	default:
		return nil, "", fmt.Errorf("export: unknown format %q", string(format))
	}
}

// validateBundlePath rejects anything that could escape the bundle root.
func validateBundlePath(bundlePath string) error {
	if bundlePath == "" || strings.HasPrefix(bundlePath, "/") || strings.Contains(bundlePath, "\\") {
		return fmt.Errorf("export: invalid bundle path %q", bundlePath)
	}
	for _, seg := range strings.Split(bundlePath, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("export: invalid bundle path %q", bundlePath)
		}
	}
	return nil
}

// folderSink writes the staged tree that is renamed to the final directory.
type folderSink struct {
	root string
}

var _ bundleSink = (*folderSink)(nil)
var _ directPlacer = (*folderSink)(nil)

// placePath maps a bundle path into the staging tree, creating its parents.
func (f *folderSink) placePath(bundlePath string) (string, error) {
	if err := validateBundlePath(bundlePath); err != nil {
		return "", err
	}
	dest := filepath.Join(f.root, filepath.FromSlash(bundlePath))
	if !sameOrInside(f.root, dest) {
		return "", fmt.Errorf("export: bundle path %q escapes the staging directory", bundlePath)
	}
	if err := os.MkdirAll(filepath.Dir(dest), fpaths.DirMode); err != nil {
		return "", err
	}
	return dest, nil
}

// addFile streams one source file into the staging tree, preserving mtime.
func (f *folderSink) addFile(ctx context.Context, bundlePath, srcPath string, kind FileKind) (string, int64, error) {
	info, err := os.Stat(srcPath)
	if err != nil {
		return "", 0, err
	}
	dest, err := f.placePath(bundlePath)
	if err != nil {
		return "", 0, err
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return "", 0, err
	}
	defer src.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fpaths.FileMode)
	if err != nil {
		return "", 0, err
	}
	sha, n, copyErr := hashStream(ctx, out, src)
	closeErr := out.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(dest)
		return "", n, copyErr
	}
	if err := os.Chtimes(dest, info.ModTime(), info.ModTime()); err != nil {
		return sha, n, err
	}
	return sha, n, nil
}

// addBytes writes generated content (meta JSON, manifest).
func (f *folderSink) addBytes(ctx context.Context, bundlePath string, data []byte) (string, int64, error) {
	dest, err := f.placePath(bundlePath)
	if err != nil {
		return "", 0, err
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if err := os.WriteFile(dest, data, fpaths.FileMode); err != nil {
		return "", 0, err
	}
	return sha256Hex(data), int64(len(data)), nil
}

func (f *folderSink) finish() error { return nil }

// sha256Hex hashes data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// copyCtx copies src to dst through a caller-owned buffer, checking ctx before
// every chunk. It never reads a whole file into memory.
func copyCtx(ctx context.Context, dst io.Writer, src io.Reader, buf []byte) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, err := dst.Write(buf[:n]); err != nil {
				return total, err
			}
			total += int64(n)
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

// hashStream copies src to dst while hashing the same bytes.
func hashStream(ctx context.Context, dst io.Writer, src io.Reader) (string, int64, error) {
	h := sha256.New()
	buf := getBuffer()
	defer putBuffer(buf)
	n, err := copyCtx(ctx, io.MultiWriter(dst, h), src, buf)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// hashFile streams a file through SHA-256 without writing anything.
func hashFile(ctx context.Context, path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	return hashStream(ctx, io.Discard, file)
}

// zeroTime is the mtime used for generated entries when the clock is zero.
var zeroTime = time.Time{}
