// Package library implements the SPEC-04 storage, query, project-link and PDF
// layers: LIT-P3 (global PDF copies), LIT-P5 (project association and browsing)
// and LIT-P6 (locating/opening PDFs). It never bypasses the foundation stores
// for writes; read-only SQL is limited to note/relation/evidence lookups that
// have no store yet (SPEC-04 §4.2, §5.3).
package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"researchos/internal/foundation/errs"
	"researchos/internal/foundation/ids"
	"researchos/internal/foundation/paths"
	"researchos/internal/foundation/workspace"
	"researchos/internal/literature"
)

// copyBufferSize is the streaming copy buffer mandated by SPEC-04 §5.4 (1 MiB).
const copyBufferSize = 1 << 20

// sourceName is the fixed file name of an imported copy (SPEC-04 §4.1).
const sourceName = "source.pdf"

// StoredFile describes one committed copy.
type StoredFile struct {
	// RelativePath is the workspace-relative path (forward slashes).
	RelativePath string
	// Hash is the lowercase hex SHA-256 of the copied bytes.
	Hash string
	// Size is the copied byte count.
	Size int64
}

// FileStore owns the PDF copies below <workspace>/library/papers/<paper-id>/.
// It never overwrites an existing copy (INV-03): replacement only happens
// through LIT-09's per-file user confirmation.
type FileStore struct {
	ws *workspace.Workspace
}

// NewFileStore binds the store to an open workspace.
func NewFileStore(ws *workspace.Workspace) *FileStore {
	return &FileStore{ws: ws}
}

// Store streams src into library/papers/<paperID>/source.pdf and returns the
// copy metadata. The copy is written to a same-directory temporary file, sized
// and fsynced, then renamed into place. Any failure removes the temporary file
// (and the directory it created) and leaves no partial target behind.
//
// sizeHint > 0 requires the stream to produce exactly that many bytes.
func (s *FileStore) Store(ctx context.Context, paperID string, src io.Reader, sizeHint int64) (StoredFile, error) {
	if s == nil || s.ws == nil {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本: 文件存储未绑定工作区")
	}
	if src == nil {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: 源读取器为空", paperID)
	}
	if err := validPaperID(paperID); err != nil {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: %w", paperID, err)
	}

	p := s.ws.Paths()
	relPath := p.PaperPDF(paperID)
	dirRel := p.PaperDir(paperID)
	dirAbs, err := p.Abs(dirRel)
	if err != nil {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: 解析目录: %w", paperID, err)
	}
	createdDir, err := ensureDir(dirAbs)
	if err != nil {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: 创建目录: %w", paperID, err)
	}
	if dirAbs, err = p.Abs(dirRel); err != nil {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: 解析目录: %w", paperID, err)
	}
	target := filepath.Join(dirAbs, sourceName)
	if _, err := os.Lstat(target); err == nil {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: 目标已存在，拒绝覆盖: %w", paperID, os.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: 检查目标: %w", paperID, err)
	}

	tmpID, err := ids.New(s.ws.Clock())
	if err != nil {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: 生成临时名: %w", paperID, err)
	}
	tmpPath := filepath.Join(dirAbs, ".source.pdf."+tmpID+".tmp")

	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, paths.FileMode)
	if err != nil {
		if createdDir {
			_ = os.Remove(dirAbs)
		}
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: 创建临时文件: %w", paperID, err)
	}

	committed := false
	closed := false
	defer func() {
		if committed {
			return
		}
		if !closed {
			_ = f.Close()
		}
		_ = os.Remove(tmpPath)
		if createdDir {
			_ = os.Remove(dirAbs)
		}
	}()

	hasher := sha256.New()
	buf := make([]byte, copyBufferSize)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: %w", paperID, err)
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: 写入临时文件: %w", paperID, err)
			}
			hasher.Write(buf[:n])
			written += int64(n)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: 读取源: %w", paperID, readErr)
		}
	}
	if sizeHint > 0 && written != sizeHint {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: 大小校验失败，期望 %d 字节，实际 %d 字节", paperID, sizeHint, written)
	}
	if err := f.Sync(); err != nil {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: fsync 临时文件: %w", paperID, err)
	}
	if err := f.Close(); err != nil {
		closed = true
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: 关闭临时文件: %w", paperID, err)
	}
	closed = true
	if err := ctx.Err(); err != nil {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: %w", paperID, err)
	}
	// Re-check right before the rename: never overwrite an existing copy.
	if _, err := os.Lstat(target); err == nil {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: 目标已存在，拒绝覆盖: %w", paperID, os.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: 检查目标: %w", paperID, err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return StoredFile{}, fmt.Errorf("library: 复制文献副本 %s: 提交副本: %w", paperID, err)
	}
	committed = true
	syncDir(dirAbs)
	return StoredFile{
		RelativePath: relPath,
		Hash:         hex.EncodeToString(hasher.Sum(nil)),
		Size:         written,
	}, nil
}

// Remove deletes the imported copy of paperID together with its directory. It is
// the compensation helper of the import flow; a missing copy is not an error.
func (s *FileStore) Remove(ctx context.Context, paperID string) error {
	if s == nil || s.ws == nil {
		return fmt.Errorf("library: 删除文献副本: 文件存储未绑定工作区")
	}
	if err := validPaperID(paperID); err != nil {
		return fmt.Errorf("library: 删除文献副本: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("library: 删除文献副本 %s: %w", paperID, err)
	}
	p := s.ws.Paths()
	target, err := p.Abs(p.PaperPDF(paperID))
	if err != nil {
		return fmt.Errorf("library: 删除文献副本 %s: 解析路径: %w", paperID, err)
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("library: 删除文献副本 %s: %w", paperID, err)
	}
	dir, err := p.Abs(p.PaperDir(paperID))
	if err != nil {
		return fmt.Errorf("library: 删除文献目录 %s: 解析路径: %w", paperID, err)
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("library: 删除文献目录 %s: %w", paperID, err)
	}
	return nil
}

// Move relocates the imported copy directory from fromPaperID to toPaperID.
// It never overwrites an existing target and keeps both paths inside the
// workspace. The import pipeline uses it after papers.Upsert has assigned the
// final id; callers are expected to be the only writer of the source id.
func (s *FileStore) Move(ctx context.Context, fromPaperID, toPaperID string) error {
	if s == nil || s.ws == nil {
		return fmt.Errorf("library: 移动文献副本: 文件存储未绑定工作区")
	}
	if err := validPaperID(fromPaperID); err != nil {
		return fmt.Errorf("library: 移动文献副本: %w", err)
	}
	if err := validPaperID(toPaperID); err != nil {
		return fmt.Errorf("library: 移动文献副本: %w", err)
	}
	if fromPaperID == toPaperID {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("library: 移动文献副本 %s -> %s: %w", fromPaperID, toPaperID, err)
	}
	p := s.ws.Paths()
	fromDir, err := p.Abs(p.PaperDir(fromPaperID))
	if err != nil {
		return fmt.Errorf("library: 移动文献副本 %s: 解析源目录: %w", fromPaperID, err)
	}
	toDir, err := p.Abs(p.PaperDir(toPaperID))
	if err != nil {
		return fmt.Errorf("library: 移动文献副本 %s: 解析目标目录: %w", toPaperID, err)
	}
	if info, err := os.Stat(fromDir); err != nil {
		return fmt.Errorf("library: 移动文献副本 %s: 源目录不可用: %w", fromPaperID, err)
	} else if !info.IsDir() {
		return fmt.Errorf("library: 移动文献副本 %s: 源路径不是目录", fromPaperID)
	}
	if _, err := os.Lstat(toDir); err == nil {
		return fmt.Errorf("library: 移动文献副本 %s -> %s: 目标已存在，拒绝覆盖: %w", fromPaperID, toPaperID, os.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("library: 移动文献副本 %s: 检查目标: %w", toPaperID, err)
	}
	if err := os.MkdirAll(filepath.Dir(toDir), paths.DirMode); err != nil {
		return fmt.Errorf("library: 移动文献副本 %s: 创建父目录: %w", toPaperID, err)
	}
	if err := os.Rename(fromDir, toDir); err != nil {
		return fmt.Errorf("library: 移动文献副本 %s -> %s: %w", fromPaperID, toPaperID, err)
	}
	syncDir(filepath.Dir(toDir))
	return nil
}

// Stat returns the absolute path and file info of the imported copy. A missing
// copy yields literature.ErrPaperFileMissing; nothing is rebuilt (INV-08).
func (s *FileStore) Stat(ctx context.Context, paperID string) (string, os.FileInfo, error) {
	if s == nil || s.ws == nil {
		return "", nil, fmt.Errorf("library: 定位文献副本: 文件存储未绑定工作区")
	}
	if err := validPaperID(paperID); err != nil {
		return "", nil, fmt.Errorf("library: 定位文献副本: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", nil, fmt.Errorf("library: 定位文献副本 %s: %w", paperID, err)
	}
	abs, err := s.ws.Paths().Abs(s.ws.Paths().PaperPDF(paperID))
	if err != nil {
		return "", nil, fmt.Errorf("library: 定位文献副本 %s: %w", paperID, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil, fmt.Errorf("library: 文献副本 %s 不存在: %w", paperID, literature.ErrPaperFileMissing)
		}
		return "", nil, fmt.Errorf("library: 读取文献副本 %s: %w", paperID, err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("library: 文献副本 %s 不是普通文件: %w", paperID, literature.ErrPaperFileMissing)
	}
	return abs, info, nil
}

// validPaperID rejects ids that must not be turned into a directory name.
func validPaperID(paperID string) error {
	if !ids.Valid(paperID) {
		return fmt.Errorf("无效的文献 ID %q: %w", paperID, errs.ErrBadEntityID)
	}
	return nil
}

// ensureDir creates dir when needed and reports whether it was created.
func ensureDir(dir string) (bool, error) {
	info, err := os.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return false, fmt.Errorf("%s 不是目录", dir)
		}
		return false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(dir, paths.DirMode); err != nil {
		return false, err
	}
	return true, nil
}

// syncDir flushes the directory entry after a rename. Unsupported platforms and
// file systems (notably Windows) are ignored: the rename itself is atomic.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
