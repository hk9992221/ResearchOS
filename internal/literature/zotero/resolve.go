package zotero

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"researchos/internal/literature"
)

// Resolver maps a Zotero attachment record to a readable local file without
// touching it. It is shared by both readers so path rules live in one place.
//
// Rules (SPEC-04 §5.2, O-10):
//   - imported_file: <DataDir>/storage/<attachment key>/<filename>
//   - linked_file "attachments:<rel>": <LinkedBaseDir>/<rel>
//   - absolute paths are used as-is
//   - storage and linked-base paths must stay inside their root; ".." escapes
//     return ErrPathOutsideStorage
//   - every resolved path must be an existing regular file, otherwise
//     ErrAttachmentUnresolved
type Resolver struct {
	// DataDir is the Zotero data directory (parent of storage/).
	DataDir string
	// LinkedBaseDir is Zotero's "Linked Attachment Base Directory"
	// (extensions.zotero.baseAttachmentPath). Empty means linked relative
	// attachments cannot be resolved.
	LinkedBaseDir string
}

// Resolve returns the readable file for att.
func (r Resolver) Resolve(att literature.ZoteroAttachment) (literature.ResolvedFile, error) {
	target, err := r.target(att)
	if err != nil {
		return literature.ResolvedFile{}, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return literature.ResolvedFile{}, fmt.Errorf("%w: %v", literature.ErrAttachmentUnresolved, err)
	}
	if !info.Mode().IsRegular() {
		return literature.ResolvedFile{}, fmt.Errorf("%w: %s 不是普通文件", literature.ErrAttachmentUnresolved, target)
	}
	return literature.ResolvedFile{Path: target, Size: info.Size(), ModTime: info.ModTime()}, nil
}

func (r Resolver) target(att literature.ZoteroAttachment) (string, error) {
	path := strings.TrimSpace(att.Path)
	lower := strings.ToLower(path)

	switch {
	case path == "":
		if strings.EqualFold(att.LinkMode, "imported_file") && att.Key != "" && att.Filename != "" {
			return r.storageTarget(att.Key, att.Filename)
		}
		return "", fmt.Errorf("%w: 附件 %s 没有路径记录", literature.ErrAttachmentUnresolved, att.Key)

	case strings.HasPrefix(lower, "storage:"):
		name := strings.TrimSpace(path[len("storage:"):])
		if name == "" {
			name = att.Filename
		}
		if att.Key == "" {
			return "", fmt.Errorf("%w: storage 附件 %s 缺少 key", literature.ErrAttachmentUnresolved, att.Filename)
		}
		return r.storageTarget(att.Key, name)

	case strings.HasPrefix(lower, "attachments:"):
		rel := strings.TrimSpace(path[len("attachments:"):])
		if rel == "" {
			return "", fmt.Errorf("%w: 附件 %s 的 attachments: 路径为空", literature.ErrAttachmentUnresolved, att.Key)
		}
		rel = filepath.FromSlash(rel)
		if filepath.IsAbs(rel) {
			return filepath.Clean(rel), nil
		}
		if strings.TrimSpace(r.LinkedBaseDir) == "" {
			return "", fmt.Errorf("%w: 未配置 Zotero 链接附件基目录", literature.ErrAttachmentUnresolved)
		}
		return r.containedJoin(r.LinkedBaseDir, rel)

	case filepath.IsAbs(filepath.FromSlash(path)):
		return filepath.Clean(filepath.FromSlash(path)), nil

	default:
		return "", fmt.Errorf("%w: 不支持的附件路径 %q", literature.ErrAttachmentUnresolved, path)
	}
}

func (r Resolver) storageTarget(key, name string) (string, error) {
	if strings.TrimSpace(r.DataDir) == "" {
		return "", fmt.Errorf("%w: 未配置 Zotero 数据目录", literature.ErrAttachmentUnresolved)
	}
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("%w: storage 附件缺少 key", literature.ErrAttachmentUnresolved)
	}
	root := filepath.Join(r.DataDir, "storage")
	return r.containedJoin(filepath.Join(root, key), filepath.FromSlash(name))
}

// containedJoin joins rel under root and rejects any path that escapes root.
func (r Resolver) containedJoin(root, rel string) (string, error) {
	root = filepath.Clean(root)
	target := filepath.Clean(filepath.Join(root, rel))
	relCheck, err := filepath.Rel(root, target)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", literature.ErrPathOutsideStorage, target)
	}
	return target, nil
}
