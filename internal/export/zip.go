package export

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	fpaths "researchos/internal/foundation/paths"
)

// zipSink streams entries into <DestDir>/.<base>.zip.part and hashes the whole
// archive on the same write path (§5.9 writer table).
type zipSink struct {
	file     *os.File
	partPath string
	zw       *zip.Writer
	hash     hash.Hash
	prefix   string
}

var _ bundleSink = (*zipSink)(nil)

// newZipSink creates the part file. A leftover part from a crashed process is
// removed first: it is exporter-owned temporary state, never a user file.
func newZipSink(partPath, prefix string) (*zipSink, error) {
	if err := os.Remove(partPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fpaths.FileMode)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	return &zipSink{
		file:     file,
		partPath: partPath,
		zw:       zip.NewWriter(io.MultiWriter(file, h)),
		hash:     h,
		prefix:   prefix,
	}, nil
}

// addFile appends one source file. PDFs are already compressed and use Store;
// text, databases and JSON use Deflate (§5.9, §10.3).
func (z *zipSink) addFile(ctx context.Context, bundlePath, srcPath string, kind FileKind) (string, int64, error) {
	info, err := os.Stat(srcPath)
	if err != nil {
		return "", 0, err
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	name, err := zipEntryName(z.prefix, bundlePath)
	if err != nil {
		return "", 0, err
	}
	header := &zip.FileHeader{
		Name:     name,
		Method:   zipMethod(bundlePath, kind),
		Modified: info.ModTime(),
	}
	header.SetMode(fpaths.FileMode)

	src, err := os.Open(srcPath)
	if err != nil {
		return "", 0, err
	}
	defer src.Close()
	dst, err := z.zw.CreateHeader(header)
	if err != nil {
		return "", 0, err
	}
	return hashStream(ctx, dst, src)
}

// addBytes appends generated content (meta JSON, manifest).
func (z *zipSink) addBytes(ctx context.Context, bundlePath string, data []byte) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	name, err := zipEntryName(z.prefix, bundlePath)
	if err != nil {
		return "", 0, err
	}
	header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: zeroTime}
	header.SetMode(fpaths.FileMode)
	dst, err := z.zw.CreateHeader(header)
	if err != nil {
		return "", 0, err
	}
	if _, err := dst.Write(data); err != nil {
		return "", 0, err
	}
	return sha256Hex(data), int64(len(data)), nil
}

// finish closes the archive, flushes it to disk and closes the part file.
func (z *zipSink) finish() error {
	closeErr := z.zw.Close()
	syncErr := z.file.Sync()
	fileErr := z.file.Close()
	return errors.Join(closeErr, syncErr, fileErr)
}

// archiveSHA is the hex digest of the bytes written to the part file.
func (z *zipSink) archiveSHA() string { return hex.EncodeToString(z.hash.Sum(nil)) }

// zipEntryName joins the archive prefix and the bundle path, rejecting
// traversal and forcing '/' separators.
func zipEntryName(prefix, bundlePath string) (string, error) {
	if err := validateBundlePath(bundlePath); err != nil {
		return "", err
	}
	return prefix + bundlePath, nil
}

// zipMethod is Store for PDFs and Deflate for everything else.
func zipMethod(bundlePath string, kind FileKind) uint16 {
	if kind == KindPaperPDF || strings.EqualFold(filepath.Ext(bundlePath), ".pdf") {
		return zip.Store
	}
	return zip.Deflate
}
