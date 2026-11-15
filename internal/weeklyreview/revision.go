package weeklyreview

// Section-level revision (SPEC-09 §5.3 局部修订): one section is rewritten from
// the same digest and skill as the whole draft, plus a block of temporary
// instructions and, optionally, the excerpt the user selected in the editor.
//
// The instructions live for the length of one call. They are never written to
// the skill store, to settings or to the report metadata: the stored note only
// names the section that was rewritten, so a later regeneration cannot inherit
// them.

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"researchos/internal/ai/output"
	"researchos/internal/ai/protocol"
	"researchos/internal/foundation/schema"
)

// Revision limits keep one revision call small. The instructions and the
// focus excerpt are bounded because they are part of the prompt sent to the
// model; an over-long paste would push the digest out of the window.
const (
	maxRevisionRunes = 800
	maxFocusRunes    = 1200
)

// SectionRevisionInput rewrites one section of an existing draft.
type SectionRevisionInput struct {
	ReportID string
	// Section is the section to rewrite; it must be one of the seven.
	Section SectionKey
	// FocusText is the part of the section the user selected, if any. It
	// narrows the revision; empty revises the whole section.
	FocusText string
	// Instructions is the temporary revision instruction. It is required
	// and applies to this call only.
	Instructions string
	// ExpectedHash is the content hash the caller read. A mismatch means
	// the draft changed elsewhere and the revision is refused.
	ExpectedHash string
	// SkillID selects the writing skill. Empty uses the workspace default.
	SkillID string
	Actor   Actor
	// Progress reports the one section as it is generated. Nil is fine.
	Progress SectionProgress
	// Delta streams the section text as it is written. Nil is fine.
	Delta SectionDelta
}

// SectionRevisionOutcome carries the rewritten section and the stored report.
type SectionRevisionOutcome struct {
	// Report is the stored draft with only that section replaced.
	Report *WeeklyReport
	// Section is the rewritten section key.
	Section SectionKey
	// Markdown is the new body of the section, without its heading.
	Markdown string
	// Violations are the findings Validate reports for the whole new body.
	Violations []Violation
	// OutputID is the AI output row of this call, for the provenance trail.
	OutputID string
}

// RegenerateSection rewrites one section of a draft with temporary
// instructions. Everything outside the section's body stays byte-identical,
// and the assembled body goes through the same Validate as a full draft.
func (s *service) RegenerateSection(ctx context.Context, in SectionRevisionInput) (*SectionRevisionOutcome, error) {
	if _, err := writeActor(in.Actor); err != nil {
		return nil, err
	}
	sec, ok := templateSection(in.Section)
	if !ok {
		return nil, serviceError(ErrInvalidInput, "未知的章节：%s", string(in.Section))
	}
	instructions := strings.TrimSpace(in.Instructions)
	focus := strings.TrimSpace(in.FocusText)
	if instructions == "" && focus == "" {
		return nil, serviceError(ErrInvalidInput, "请填写修改意见或选中要修改的片段")
	}
	if n := len([]rune(instructions)); n > maxRevisionRunes {
		return nil, serviceError(ErrInvalidInput, "修改意见过长（%d 字，上限 %d 字）", n, maxRevisionRunes)
	}
	if n := len([]rune(focus)); n > maxFocusRunes {
		return nil, serviceError(ErrInvalidInput,
			"选中的片段过长（%d 字，上限 %d 字）；请缩小选区或改用修改意见", n, maxFocusRunes)
	}
	if err := s.requireWritable(ctx); err != nil {
		return nil, err
	}
	h, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	report, err := s.reportByID(ctx, h.DB(), in.ReportID, false)
	if err != nil {
		return nil, err
	}
	if report.Status == StatusConfirmed {
		return nil, serviceError(ErrConfirmedReadOnly, "已确认周报不可修订；请先撤回为草稿")
	}
	if strings.TrimSpace(report.ContentMD) == "" {
		return nil, serviceError(ErrInvalidInput, "报告尚无正文，无法按节修订")
	}
	if in.ExpectedHash != "" && in.ExpectedHash != report.ContentHash {
		return nil, serviceError(ErrContentConflict, "内容已在别处修改").
			withDetails(map[string]any{"updated_at": report.UpdatedAt, "content_hash": report.ContentHash})
	}
	if s.ai == nil {
		return nil, serviceError(ErrAIUnavailable, "AI 服务未配置，无法按节修订")
	}
	digest, err := s.CollectActivities(ctx, CollectInput{Period: report.Period})
	if err != nil {
		return nil, err
	}
	info, err := s.projectInfo(ctx)
	if err != nil {
		return nil, err
	}
	skill, err := s.resolveSkill(ctx, in.SkillID)
	if err != nil {
		return nil, err
	}

	current, ok := sectionBody(report.ContentMD, sec.Key)
	if !ok {
		return nil, serviceError(ErrInvalidInput, "报告中找不到「%s」这一节", sec.Title)
	}
	if in.Progress != nil {
		in.Progress(0, 1, sec.Key)
	}
	req := output.GenerateRequest{
		ProjectID:       s.projectID,
		TaskKind:        taskKind,
		PromptVersion:   promptVersion,
		Messages:        sectionRevisionMessages(info, digest, sec, skill, current, focus, instructions),
		SourceRefs:      sectionSourceRefs(digest, sec.Key),
		Entity:          &output.EntityRef{Type: string(schema.EntityWeeklyReport), ID: report.ID},
		MaxOutputTokens: sectionMaxTokens,
	}
	if in.Delta != nil {
		key := sec.Key
		req.OnDelta = func(text string) { in.Delta(key, text) }
	}
	res, err := s.ai.Generate(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, serviceError(ErrAIUnavailable, "AI 未能重写本节：%v", err)
	}
	lines, err := normalizeSectionOutput(res.Output.ContentMD, sec)
	if err != nil {
		return nil, serviceError(ErrAIOutputInvalid, "%s：%v", sec.Title, err)
	}
	markdown := strings.Join(lines, "\n")
	assembled, err := replaceSectionBody(report.ContentMD, sec.Key, lines)
	if err != nil {
		return nil, serviceError(ErrInvalidInput, "替换本节正文失败：%v", err)
	}
	if len(assembled) > maxContentBytes {
		return nil, serviceError(ErrContentTooLarge, "修订后的正文超过 512 KiB 上限")
	}
	violations := Validate(assembled, defaultTemplate, digest)

	now := s.clock.Now().UTC()
	note := revisionNote(sec, res.Output.ID)
	err = h.InTx(ctx, func(tx *sql.Tx) error {
		cur, err := s.reportTx(ctx, tx, report.ID, false)
		if err != nil {
			return err
		}
		if cur.Status == StatusConfirmed {
			return serviceError(ErrConfirmedReadOnly, "已确认周报不可修订")
		}
		if in.ExpectedHash != "" && cur.ContentHash != in.ExpectedHash {
			return serviceError(ErrContentConflict, "内容已在别处修改")
		}
		cur.ContentMD = assembled
		cur.ContentHash = contentHash(assembled)
		cur.UpdatedAt = now
		meta := cur.Meta
		if meta.Generation != nil {
			meta.Generation.Notes = appendNote(meta.Generation.Notes, note)
		}
		meta.Checks = buildCheckState(mergeAcks(violations, cur.Meta.Checks), now)
		cur.Meta = meta
		return updateReport(ctx, tx, cur)
	})
	if err != nil {
		return nil, err
	}
	stored, err := s.getReport(ctx, report.ID)
	if err != nil {
		return nil, err
	}
	return &SectionRevisionOutcome{
		Report:     stored,
		Section:    sec.Key,
		Markdown:   markdown,
		Violations: violations,
		OutputID:   res.Output.ID,
	}, nil
}

// revisionNote records which section a temporary revision rewrote. It names
// the section and the output, never the instructions themselves.
func revisionNote(sec TemplateSection, outputID string) string {
	note := "第" + strconv.Itoa(sec.Num) + "节「" + sec.Title + "」按临时修改意见重写"
	if outputID != "" {
		note += "（output " + outputID + "）"
	}
	return note
}

// appendNote adds one line to the generation notes, keeping what was there.
func appendNote(existing, note string) string {
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return note
	}
	return existing + "\n" + note
}

// sectionRevisionMessages is the prompt of one revision call: the same
// section prompt as generation, plus what is being revised and why.
func sectionRevisionMessages(info projectInfo, digest *ActivityDigest, sec TemplateSection, skill Skill, current, focus, instructions string) []protocol.ChatMessage {
	msgs := sectionMessages(info, digest, sec, skill)
	if len(msgs) == 0 {
		return nil
	}
	last := len(msgs) - 1
	var b strings.Builder
	b.WriteString(msgs[last].Content)
	b.WriteString("\n本次不是从零写作，而是修订下面这一节的既有正文。\n\n当前这一节的正文：\n")
	b.WriteString(current)
	b.WriteString("\n")
	if focus != "" {
		b.WriteString("\n用户选中的片段（本次修订的重点；只改与它相关的表述，不要牵动整节的其余部分）：\n<<<\n")
		b.WriteString(focus)
		b.WriteString("\n>>>\n")
	}
	b.WriteString("\n本轮临时修改意见（只适用于本次生成，不是长期写作规范）：\n")
	if instructions == "" {
		b.WriteString("（未填写，按选中片段与既有正文自行判断）")
	} else {
		b.WriteString(instructions)
	}
	b.WriteString("\n\n输出要求：给出该节修订后的完整正文，不要输出标题、不要解释改动；仍未改动的条目照原样保留；引用格式与硬性约束不变。\n")
	msgs[last].Content = b.String()
	return msgs
}

// sectionBody returns the body text of one section, without its heading.
func sectionBody(content string, key SectionKey) (string, bool) {
	lines := splitLines(content)
	start, end, ok := sectionRegion(lines, key)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(strings.Join(lines[start+1:end], "\n")), true
}

// replaceSectionBody swaps the body lines of one section and leaves every
// other byte of the document alone. The blank lines that closed the old body
// are kept, so the surrounding layout does not shift.
func replaceSectionBody(content string, key SectionKey, lines []string) (string, error) {
	doc := splitLines(content)
	start, end, ok := sectionRegion(doc, key)
	if !ok {
		return "", serviceError(ErrInvalidInput, "找不到章节「%s」", sectionTitle(key))
	}
	trailing := 0
	for i := end - 1; i > start; i-- {
		if strings.TrimSpace(doc[i]) != "" {
			break
		}
		trailing++
	}
	out := make([]string, 0, len(doc)+len(lines))
	out = append(out, doc[:start+1]...)
	out = append(out, lines...)
	for range trailing {
		out = append(out, "")
	}
	out = append(out, doc[end:]...)
	joined := strings.Join(out, "\n")
	if strings.HasSuffix(content, "\n") {
		joined += "\n"
	}
	return joined, nil
}

// sectionRegion finds the heading line of a section and the line range of its
// body: from the line after the heading up to the next heading, or the end of
// the document.
func sectionRegion(lines []string, key SectionKey) (start, end int, ok bool) {
	start = -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "##") || strings.HasPrefix(trimmed, "###") {
			continue
		}
		got, known := normalizeHeading(trimmed)
		if known && got == key && start < 0 {
			start = i
			continue
		}
		if start >= 0 {
			end = i
			return start, end, true
		}
	}
	if start < 0 {
		return 0, 0, false
	}
	return start, len(lines), true
}

// splitLines splits content into lines, dropping the empty element a final
// newline produces so a join with "\n" reproduces the document byte for byte.
func splitLines(content string) []string {
	lines := strings.Split(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}
