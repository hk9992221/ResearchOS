package search

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"runtime"

	"researchos/internal/index/store"
)

// jumpTarget resolves the navigation target of one result. It only reads; no
// Activity is written and no entity is modified (SPEC-10 §7.1).
func (s *Service) jumpTarget(ctx context.Context, t *searchTarget, result SearchResult) (JumpTarget, error) {
	target := JumpTarget{
		ProjectID: result.ProjectID,
		EntityID:  result.EntityID,
		Locator:   result.Locator,
	}
	switch result.EntityType {
	case EntityPaper:
		target.Kind = "paper"
		target.PaperID = result.EntityID
		if path, err := s.paperPDFPath(ctx, result.EntityID); err == nil {
			target.PDFPath = path
		}
	case EntityNote:
		target.Kind = "note"
		if result.Source == SourcePaperNote {
			if paperID, err := s.notePaperID(ctx, t, result.EntityID); err == nil {
				target.PaperID = paperID
				if path, err := s.paperPDFPath(ctx, paperID); err == nil {
					target.PDFPath = path
				}
			}
		}
	case EntityDBRecord:
		target.Kind = "db_record"
	case EntityIdea:
		target.Kind = "idea"
	case EntityExperiment:
		target.Kind = "experiment"
	default:
		target.Kind = string(result.EntityType)
	}
	return target, nil
}

// notePaperID returns the global paper of a paper note (notes.paper_id, added
// by the SPEC-04 migration).
func (s *Service) notePaperID(ctx context.Context, t *searchTarget, noteID string) (string, error) {
	if t == nil || t.partition != store.PartitionProject {
		return "", nil
	}
	var paperID sql.NullString
	err := t.db.QueryRowContext(ctx, `SELECT paper_id FROM notes WHERE id = ?`, noteID).Scan(&paperID)
	if err != nil {
		return "", err
	}
	if !paperID.Valid {
		return "", nil
	}
	return paperID.String, nil
}

// paperPDFPath returns the absolute path of a paper's local PDF copy.
func (s *Service) paperPDFPath(ctx context.Context, paperID string) (string, error) {
	if paperID == "" {
		return "", nil
	}
	var rel sql.NullString
	err := s.ws.Global().DB().QueryRowContext(ctx, `SELECT file_path FROM papers WHERE id = ?`, paperID).Scan(&rel)
	if errors.Is(err, sql.ErrNoRows) || !rel.Valid || rel.String == "" {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return s.ws.Paths().Abs(rel.String)
}

// ResolveTarget resolves an arbitrary entity reference plus a locator into an
// executable jump target (SPEC-10 §5.2). Only references that exist resolve.
func (s *Service) ResolveTarget(ctx context.Context, ref EntityRef, loc Locator) (JumpTarget, error) {
	if !ref.EntityType.Valid() {
		return JumpTarget{}, Errorf(CodeInvalidRequest, "未知实体类型 %q", string(ref.EntityType))
	}
	if ref.EntityID == "" {
		return JumpTarget{}, Errorf(CodeInvalidRequest, "实体 ID 为空")
	}
	switch ref.EntityType {
	case EntityPaper:
		path, err := s.paperPDFPath(ctx, ref.EntityID)
		if err != nil {
			return JumpTarget{}, err
		}
		exists, err := s.paperExists(ctx, ref.EntityID)
		if err != nil {
			return JumpTarget{}, err
		}
		if !exists {
			return JumpTarget{}, WrapError(CodeTargetNotFound, "文献不存在", ErrEntityNotFound)
		}
		return JumpTarget{Kind: "paper", EntityID: ref.EntityID, PaperID: ref.EntityID, Locator: loc, PDFPath: path}, nil
	default:
		if ref.ProjectID == "" {
			return JumpTarget{}, Errorf(CodeInvalidRequest, "项目实体需要 project_id")
		}
		handle, err := s.ws.Project(ctx, ref.ProjectID)
		if err != nil {
			return JumpTarget{}, WrapError(CodeScopeNotFound, "项目不存在", err)
		}
		var paperID string
		if err := s.entityExists(ctx, ref, handle.DB(), &paperID); err != nil {
			return JumpTarget{}, err
		}
		target := JumpTarget{Kind: kindOf(ref.EntityType), ProjectID: ref.ProjectID, EntityID: ref.EntityID, Locator: loc}
		if ref.EntityType == EntityNote && paperID != "" {
			target.PaperID = paperID
			if path, err := s.paperPDFPath(ctx, paperID); err == nil {
				target.PDFPath = path
			}
		}
		return target, nil
	}
}

// kindOf maps an entity type to its jump kind.
func kindOf(et EntityType) string {
	switch et {
	case EntityNote:
		return "note"
	case EntityDBRecord:
		return "db_record"
	case EntityIdea:
		return "idea"
	case EntityExperiment:
		return "experiment"
	case EntityPaper:
		return "paper"
	default:
		return string(et)
	}
}

// paperExists reports whether the global paper row exists.
func (s *Service) paperExists(ctx context.Context, paperID string) (bool, error) {
	var one int
	err := s.ws.Global().DB().QueryRowContext(ctx, `SELECT 1 FROM papers WHERE id = ?`, paperID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("search: 读取文献: %w", err)
	}
	return true, nil
}

// entityExists verifies one project entity; for notes it also returns paper_id.
func (s *Service) entityExists(ctx context.Context, ref EntityRef, db *sql.DB, paperID *string) error {
	var query string
	switch ref.EntityType {
	case EntityNote:
		query = `SELECT paper_id FROM notes WHERE id = ? AND deleted_at IS NULL`
		var pid sql.NullString
		err := db.QueryRowContext(ctx, query, ref.EntityID).Scan(&pid)
		if errors.Is(err, sql.ErrNoRows) {
			return WrapError(CodeTargetNotFound, "笔记不存在", ErrEntityNotFound)
		}
		if err != nil {
			return fmt.Errorf("search: 读取笔记: %w", err)
		}
		if pid.Valid {
			*paperID = pid.String
		}
		return nil
	case EntityDBRecord:
		query = `SELECT 1 FROM db_records WHERE id = ? AND deleted_at IS NULL`
	case EntityIdea:
		query = `SELECT 1 FROM ideas WHERE id = ? AND deleted_at IS NULL`
	case EntityExperiment:
		query = `SELECT 1 FROM experiments WHERE id = ? AND deleted_at IS NULL`
	default:
		return Errorf(CodeInvalidRequest, "不支持解析的实体类型 %q", string(ref.EntityType))
	}
	var one int
	err := db.QueryRowContext(ctx, query, ref.EntityID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return WrapError(CodeTargetNotFound, "目标实体不存在", ErrEntityNotFound)
	}
	if err != nil {
		return fmt.Errorf("search: 读取目标实体: %w", err)
	}
	return nil
}

// OpenTarget performs the navigation side effect (SPEC-10 §5.2). A PDF target
// opens the local copy in the external reader; when a deep link is unavailable
// the file still opens and the caller keeps the page number from
// target.Locator (documented degradation, O5). Non-PDF targets require an
// injected Navigator. Opening never writes Activity and never changes data.
func (s *Service) OpenTarget(ctx context.Context, target JumpTarget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if target.Kind == "paper" || target.Kind == "note" {
		if target.PDFPath == "" && target.PaperID == "" {
			// A plain note without a paper is an in-app target.
		} else if target.PDFPath != "" {
			if s.cfg.deepLink != nil {
				ok, err := s.cfg.deepLink(ctx, target)
				if err != nil {
					if s.cfg.Logger != nil {
						s.cfg.Logger.Warn("search: 深链打开失败，回退打开文件", "pdf", target.PDFPath, "error", err)
					}
				} else if ok {
					return nil
				}
			}
			if err := s.cfg.opener(ctx, target.PDFPath); err != nil {
				return WrapError(CodeOpenFailed, fmt.Sprintf("打开 PDF 失败（第 %d 页）", target.Locator.PageNo), err)
			}
			return nil
		}
	}
	if s.cfg.navigator != nil {
		if err := s.cfg.navigator(ctx, target); err != nil {
			return WrapError(CodeOpenFailed, "应用内跳转失败", err)
		}
		return nil
	}
	return WrapError(CodeOpenUnsupported,
		fmt.Sprintf("当前环境不支持跳转到 %s（%s）", target.Kind, describeLocator(target.Locator)), ErrOpenUnsupported)
}

// describeLocator renders the page/heading information shown when deep links
// are unavailable.
func describeLocator(loc Locator) string {
	switch {
	case loc.PageNo > 0:
		return fmt.Sprintf("第 %d 页", loc.PageNo)
	case loc.Heading != "":
		return loc.Heading
	default:
		return "文档开头"
	}
}

// SystemOpener opens a path with the OS default application.
func SystemOpener(ctx context.Context, target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "cmd", "/c", "start", "", target)
	case "darwin":
		cmd = exec.CommandContext(ctx, "open", target)
	default:
		cmd = exec.CommandContext(ctx, "xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("search: 启动外部程序: %w", err)
	}
	return nil
}
