package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"researchos/internal/foundation/papers"
	"researchos/internal/foundation/schema"
	"researchos/internal/literature"
)

const (
	settingPDFOpenMode = "literature.pdf_open_mode"
	modeSystemDefault  = "system_default"
	modeZoteroReader   = "zotero_reader"
	zoteroSelectPrefix = "zotero://select/library/items/"
)

// Opener launches the external application responsible for a path or URI
// (PDFService §5.6). It is injected so tests observe the exact target.
type Opener func(ctx context.Context, target string) error

// Locate resolves a PDF without modifying anything. imported_copy reports the
// stored hash; zotero_source never hashes the original (SPEC-04 §5.6).
func (s *Service) Locate(ctx context.Context, paperID string, target literature.PDFTarget) (literature.PDFLocation, error) {
	if err := validPaperID(paperID); err != nil {
		return literature.PDFLocation{}, fmt.Errorf("library: 定位 PDF: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return literature.PDFLocation{}, fmt.Errorf("library: 定位 PDF: %w", err)
	}
	row, err := s.papers.Get(ctx, paperID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return literature.PDFLocation{}, fmt.Errorf("library: 定位文献 %s: %w", paperID, literature.ErrPaperNotFound)
		}
		return literature.PDFLocation{}, fmt.Errorf("library: 定位文献 %s: %w", paperID, err)
	}
	switch target {
	case literature.PDFImportedCopy, "":
		abs, info, err := s.files.Stat(ctx, paperID)
		if err != nil {
			return literature.PDFLocation{}, fmt.Errorf("library: 定位文献副本 %s: %w", paperID, err)
		}
		return literature.PDFLocation{
			PaperID: paperID,
			Path:    abs,
			Exists:  true,
			Size:    info.Size(),
			Hash:    row.FileHash,
		}, nil
	case literature.PDFZoteroSource:
		return s.locateZoteroSource(ctx, row)
	default:
		return literature.PDFLocation{}, fmt.Errorf("library: 未知的 PDF 目标 %q", target)
	}
}

// Open launches the configured application. The workspace setting
// literature.pdf_open_mode selects between the imported copy / explicit target
// (system_default) and Zotero's item selection (zotero_reader). A failed Zotero
// launch falls back to the system default opener. Opening never writes Activity
// (SPEC-00 §6, AC-23).
func (s *Service) Open(ctx context.Context, paperID string, target literature.PDFTarget) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("library: 打开 PDF: %w", err)
	}
	if target == "" {
		target = literature.PDFImportedCopy
	}
	mode, err := s.openMode(ctx)
	if err != nil {
		return err
	}
	if mode == modeZoteroReader {
		uriErr := s.openInZotero(ctx, paperID)
		if uriErr == nil {
			return nil
		}
		location, locateErr := s.Locate(ctx, paperID, literature.PDFImportedCopy)
		if locateErr != nil {
			return fmt.Errorf("library: 打开 PDF: 唤起 Zotero 失败（%v），回退打开副本也失败: %w", uriErr, locateErr)
		}
		if openErr := s.opener(ctx, location.Path); openErr != nil {
			return fmt.Errorf("library: 打开 PDF: 唤起 Zotero 失败（%v），回退打开副本失败: %w", uriErr, openErr)
		}
		return nil
	}
	location, err := s.Locate(ctx, paperID, target)
	if err != nil {
		return err
	}
	if err := s.opener(ctx, location.Path); err != nil {
		return fmt.Errorf("library: 打开 PDF %s: %w", location.Path, err)
	}
	return nil
}

// OpenWith lets the user open the imported copy in an application they pick.
// Windows shows the system "Open with" dialog (shell32!OpenAs_RunDLL), which
// lists every installed application. macOS and Linux have no equivalent
// chooser a process may open, so they hand the copy to the system default
// application and the user picks another one from the desktop (the Finder's
// "Open With", the desktop's own menu); the setting literature.pdf_open_mode
// does not apply here, because this action is explicitly about another app.
func (s *Service) OpenWith(ctx context.Context, paperID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("library: 选择打开方式: %w", err)
	}
	location, err := s.Locate(ctx, paperID, literature.PDFImportedCopy)
	if err != nil {
		return err
	}
	if err := openWith(ctx, location.Path); err != nil {
		return fmt.Errorf("library: 选择打开方式 %s: %w", location.Path, err)
	}
	return nil
}

// openWith runs the platform's chooser for one path. It uses a controlled
// command per OS (no cgo), like systemOpener: Windows opens the system "Open
// with" dialog; the other platforms fall back to the default application.
func openWith(ctx context.Context, target string) error {
	if strings.TrimSpace(target) == "" {
		return errors.New("library: 打开目标为空")
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "rundll32.exe", "shell32.dll,OpenAs_RunDLL", target)
	} else {
		return systemOpener()(ctx, target)
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("library: 调用系统打开方式选择框: %w", err)
	}
	return nil
}

func (s *Service) locateZoteroSource(ctx context.Context, row papers.Paper) (literature.PDFLocation, error) {
	if s.zotero == nil {
		return literature.PDFLocation{}, fmt.Errorf("library: 定位 Zotero 原件 %s: %w", row.ID, literature.ErrZoteroNotConfigured)
	}
	if row.SourceState == schema.PaperSourceMissing || row.SourceState == schema.PaperSourceUnreadable {
		return literature.PDFLocation{}, fmt.Errorf("library: 定位 Zotero 原件 %s: 源状态为 %s: %w",
			row.ID, row.SourceState, literature.ErrAttachmentUnresolved)
	}
	reader, err := s.zotero.Reader(ctx)
	if err != nil {
		return literature.PDFLocation{}, fmt.Errorf("library: 读取 Zotero 元数据: %w", err)
	}
	libraryID := row.ZoteroLibraryID
	if libraryID == "" {
		libraryID = "0"
	}
	attachments, err := reader.ListAttachments(ctx, libraryID, row.ZoteroKey)
	if err != nil {
		return literature.PDFLocation{}, fmt.Errorf("library: 列出 Zotero 附件 %s: %w", row.ZoteroKey, err)
	}
	attachment, err := pickAttachment(attachments, row.AttachmentKey)
	if err != nil {
		return literature.PDFLocation{}, err
	}
	resolved, err := reader.ResolveAttachment(ctx, attachment)
	if err != nil {
		return literature.PDFLocation{}, fmt.Errorf("library: 解析 Zotero 附件 %s: %w", attachment.Key, err)
	}
	return literature.PDFLocation{
		PaperID: row.ID,
		Path:    resolved.Path,
		Exists:  true,
		Size:    resolved.Size,
	}, nil
}

// pickAttachment prefers the recorded attachment key and otherwise accepts only
// an unambiguous PDF attachment (§5.4: never guess).
func pickAttachment(attachments []literature.ZoteroAttachment, key string) (literature.ZoteroAttachment, error) {
	if key != "" {
		for _, attachment := range attachments {
			if attachment.Key == key {
				return attachment, nil
			}
		}
		return literature.ZoteroAttachment{}, fmt.Errorf("library: Zotero 附件 %s: %w", key, literature.ErrAttachmentUnresolved)
	}
	pdfs := make([]literature.ZoteroAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		if isPDFAttachment(attachment) {
			pdfs = append(pdfs, attachment)
		}
	}
	switch len(pdfs) {
	case 0:
		return literature.ZoteroAttachment{}, fmt.Errorf("library: 条目没有 PDF 附件: %w", literature.ErrNoPDFAttachment)
	case 1:
		return pdfs[0], nil
	default:
		return literature.ZoteroAttachment{}, fmt.Errorf("library: 条目有 %d 个 PDF 附件，需要选择: %w", len(pdfs), literature.ErrAttachmentChoice)
	}
}

func isPDFAttachment(a literature.ZoteroAttachment) bool {
	if strings.EqualFold(a.ContentType, "application/pdf") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(a.Filename), ".pdf")
}

func (s *Service) openMode(ctx context.Context) (string, error) {
	value, ok, err := s.settings.Get(ctx, settingPDFOpenMode)
	if err != nil {
		return "", fmt.Errorf("library: 读取设置 %s: %w", settingPDFOpenMode, err)
	}
	if !ok {
		return modeSystemDefault, nil
	}
	switch value {
	case modeSystemDefault, modeZoteroReader:
		return value, nil
	default:
		return modeSystemDefault, nil
	}
}

func (s *Service) openInZotero(ctx context.Context, paperID string) error {
	row, err := s.papers.Get(ctx, paperID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("library: 在 Zotero 中打开 %s: %w", paperID, literature.ErrPaperNotFound)
		}
		return fmt.Errorf("library: 在 Zotero 中打开 %s: %w", paperID, err)
	}
	if row.ZoteroKey == "" {
		return fmt.Errorf("library: 在 Zotero 中打开 %s: 缺少 Zotero key", paperID)
	}
	if err := s.opener(ctx, zoteroSelectPrefix+row.ZoteroKey); err != nil {
		return fmt.Errorf("library: 唤起 Zotero: %w", err)
	}
	return nil
}

// systemOpener is the production Opener. It uses a controlled command per OS
// (no cgo) and never retries.
func systemOpener() Opener {
	return func(ctx context.Context, target string) error {
		if strings.TrimSpace(target) == "" {
			return errors.New("library: 打开目标为空")
		}
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "windows":
			cmd = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", target)
		case "darwin":
			cmd = exec.CommandContext(ctx, "open", target)
		default:
			cmd = exec.CommandContext(ctx, "xdg-open", target)
		}
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("library: 调用系统默认打开方式: %w", err)
		}
		return nil
	}
}
