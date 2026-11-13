package export

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"

	fpaths "researchos/internal/foundation/paths"
)

// tarGzSink streams entries into <DestDir>/.<base>.tar.gz.part. gzip BestSpeed
// applies to every entry: tar has no per-entry storage policy (§5.9, §10.3).
type tarGzSink struct {
	file     *os.File
	partPath string
	gz       *gzip.Writer
	tw       *tar.Writer
	hash     hash.Hash
	prefix   string
}

var _ bundleSink = (*tarGzSink)(nil)

// newTarGzSink creates the part file, removing a crashed run's leftover first.
func newTarGzSink(partPath, prefix string) (*tarGzSink, error) {
	if err := os.Remove(partPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fpaths.FileMode)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	gz, err := gzip.NewWriterLevel(io.MultiWriter(file, h), gzip.BestSpeed)
	if err != nil {
		file.Close()
		return nil, err
	}
	return &tarGzSink{
		file:     file,
		partPath: partPath,
		gz:       gz,
		tw:       tar.NewWriter(gz),
		hash:     h,
		prefix:   prefix,
	}, nil
}

// addFile appends one source file as a regular entry.
func (t *tarGzSink) addFile(ctx context.Context, bundlePath, srcPath string, kind FileKind) (string, int64, error) {
	info, err := os.Stat(srcPath)
	if err != nil {
		return "", 0, err
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	name, err := zipEntryName(t.prefix, bundlePath)
	if err != nil {
		return "", 0, err
	}
	src, err := os.Open(srcPath)
	if err != nil {
		return "", 0, err
	}
	defer src.Close()
	if err := t.tw.WriteHeader(&tar.Header{
		Name:     name,
		Typeflag: tar.TypeReg,
		Mode:     int64(fpaths.FileMode),
		Size:     info.Size(),
		ModTime:  info.ModTime(),
	}); err != nil {
		return "", 0, err
	}
	sha, n, err := hashStream(ctx, t.tw, src)
	if err != nil {
		return "", n, err
	}
	if n != info.Size() {
		// tar headers carry the size up front; a changed source would corrupt
		// the archive rather than merely produce a stale copy.
		return "", n, fmt.Errorf("export: %s changed size during copy", bundlePath)
	}
	return sha, n, nil
}

// addBytes appends generated content.
func (t *tarGzSink) addBytes(ctx context.Context, bundlePath string, data []byte) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	name, err := zipEntryName(t.prefix, bundlePath)
	if err != nil {
		return "", 0, err
	}
	if err := t.tw.WriteHeader(&tar.Header{
		Name:     name,
		Typeflag: tar.TypeReg,
		Mode:     int64(fpaths.FileMode),
		Size:     int64(len(data)),
		ModTime:  zeroTime,
	}); err != nil {
		return "", 0, err
	}
	if _, err := t.tw.Write(data); err != nil {
		return "", 0, err
	}
	return sha256Hex(data), int64(len(data)), nil
}

// finish closes the tar end-of-archive marker, the gzip stream and the file.
func (t *tarGzSink) finish() error {
	tarErr := t.tw.Close()
	gzErr := t.gz.Close()
	syncErr := t.file.Sync()
	fileErr := t.file.Close()
	return errors.Join(tarErr, gzErr, syncErr, fileErr)
}

// archiveSHA is the hex digest of the compressed bytes.
func (t *tarGzSink) archiveSHA() string { return hex.EncodeToString(t.hash.Sum(nil)) }
