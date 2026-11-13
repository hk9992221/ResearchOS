package weeklyreview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"researchos/internal/ai/output"
	"researchos/internal/ai/protocol"
	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
)

// GenerateDraft (re)generates one report draft (SPEC-09 §5.3).
func (s *service) GenerateDraft(ctx context.Context, in GenerateInput) (*GenerateOutcome, error) {
	if _, err := writeActor(in.Actor); err != nil {
		return nil, err
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
		return nil, serviceError(ErrConfirmedReadOnly, "已确认周报不可重新生成；请先撤回为草稿")
	}
	if !in.Replace && strings.TrimSpace(report.ContentMD) != "" {
		return nil, serviceError(ErrReportNotEmpty, "报告已有内容；覆盖需显式 Replace")
	}
	if in.Replace && in.Actor == ActorMCP && strings.TrimSpace(report.ContentMD) != "" && hasManualTrace(report) {
		return nil, serviceError(ErrConfirmationRequired, "覆盖人工编辑内容需由应用内用户确认").
			withDetails(map[string]any{"report_id": report.ID})
	}
	info, err := s.projectInfo(ctx)
	if err != nil {
		return nil, err
	}
	digest, err := s.CollectActivities(ctx, CollectInput{Period: report.Period})
	if err != nil {
		return nil, err
	}

	outcome := &GenerateOutcome{Report: report, Digest: digest}
	if s.ai == nil {
		meta := manualGeneration()
		meta.AIStatus = "failed"
		meta.Error = "AI 服务未配置"
		content := report.ContentMD
		if strings.TrimSpace(content) == "" {
			content = BuildSkeleton(info.Name, report.Period, digest)
		}
		updated, uerr := s.storeDraft(ctx, h, report.ID, content, &meta, digest, nil, false)
		if uerr != nil {
			return outcome, uerr
		}
		outcome.Report = updated
		return outcome, serviceError(ErrAIUnavailable, "AI 服务未配置")
	}

	req := output.GenerateRequest{
		ProjectID:     s.projectID,
		TaskKind:      taskKind,
		PromptVersion: promptVersion,
		Messages:      buildPromptMessages(info, digest),
		SourceRefs:    digestSourceRefs(digest, 200),
		Entity:        &output.EntityRef{Type: string(schema.EntityWeeklyReport), ID: report.ID},
	}

	var (
		last     output.GenerateResult
		genErr   error
		attempts int
		parsed   string
		parseErr error
	)
	for attempts = 1; attempts <= 2; attempts++ {
		last, genErr = s.ai.Generate(ctx, req)
		if genErr != nil {
			break
		}
		parsed, parseErr = normalizeGenerated(last.Output.ContentMD)
		if parseErr == nil {
			break
		}
	}
	if attempts > 2 {
		attempts = 2
	}

	if genErr != nil {
		meta := generationFrom(last, digest)
		meta.Attempts = attempts
		meta.AIStatus = "failed"
		meta.Error = genErr.Error()
		content := report.ContentMD
		if strings.TrimSpace(content) == "" {
			content = BuildSkeleton(info.Name, report.Period, digest)
		}
		updated, uerr := s.storeDraft(ctx, h, report.ID, content, &meta, digest, nil, false)
		if uerr != nil {
			return outcome, uerr
		}
		outcome.Report = updated
		return outcome, serviceError(ErrAIUnavailable, "AI 生成失败：%v", genErr)
	}

	if parseErr != nil {
		meta := generationFrom(last, digest)
		meta.Attempts = 2
		meta.AIStatus = "failed"
		meta.Error = "AI 输出结构不符合七节模板：" + parseErr.Error()
		content := BuildSkeleton(info.Name, report.Period, digest)
		updated, uerr := s.storeDraft(ctx, h, report.ID, content, &meta, digest, nil, false)
		if uerr != nil {
			return outcome, uerr
		}
		outcome.Report = updated
		return outcome, serviceError(ErrAIOutputInvalid, "%s", meta.Error)
	}

	violations := Validate(parsed, defaultTemplate, digest)
	meta := generationFrom(last, digest)
	meta.Attempts = attempts
	meta.AIStatus = "ok"
	updated, err := s.storeDraft(ctx, h, report.ID, parsed, &meta, digest, violations, true)
	if err != nil {
		return outcome, err
	}
	outcome.Report = updated
	outcome.Violations = violations
	return outcome, nil
}

// GenerateForPeriod is the "generate this week" entry shared by UI and MCP.
func (s *service) GenerateForPeriod(ctx context.Context, in GeneratePeriodInput) (*GenerateOutcome, error) {
	actor, err := writeActor(in.Actor)
	if err != nil {
		return nil, err
	}
	cfg := s.loadSettings(ctx)
	if err := validatePeriod(in.Period, cfg.Location); err != nil {
		return nil, err
	}
	if err := s.requireWritable(ctx); err != nil {
		return nil, err
	}
	h, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}

	start, end := in.Period.StartDate(), in.Period.EndDate()
	rows, err := h.DB().QueryContext(ctx,
		`SELECT id, status FROM weekly_reports
		  WHERE period_start = ? AND period_end = ? AND deleted_at IS NULL
		  ORDER BY created_at, id`, start, end)
	if err != nil {
		return nil, fmt.Errorf("weeklyreview: find report for period: %w", err)
	}
	var ids, statuses []string
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			rows.Close()
			return nil, fmt.Errorf("weeklyreview: find report for period: %w", err)
		}
		ids = append(ids, id)
		statuses = append(statuses, status)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("weeklyreview: find report for period: %w", err)
	}

	var (
		report  *WeeklyReport
		created bool
	)
	switch len(ids) {
	case 0:
		created = true
		report, err = s.CreateReport(ctx, CreateReportInput{
			Period: in.Period, Skeleton: false, Force: false, Actor: Actor(actor),
		})
		if err != nil {
			return nil, err
		}
	case 1:
		if Status(statuses[0]) == StatusConfirmed {
			return nil, serviceError(ErrConfirmedReadOnly, "该周期周报已确认；请先撤回为草稿")
		}
		report, err = s.reportByID(ctx, h.DB(), ids[0], false)
		if err != nil {
			return nil, err
		}
	default:
		return nil, serviceError(ErrPeriodOccupied, "该周期存在多份周报，请指定报告 ID").
			withDetails(map[string]any{"report_ids": ids})
	}

	digest, derr := s.CollectActivities(ctx, CollectInput{Period: in.Period})
	if derr != nil {
		return nil, derr
	}
	if !in.IncludeAI {
		if strings.TrimSpace(report.ContentMD) == "" {
			info, ierr := s.projectInfo(ctx)
			if ierr != nil {
				return nil, ierr
			}
			meta := manualGeneration()
			report, err = s.storeDraft(ctx, h, report.ID, BuildSkeleton(info.Name, in.Period, digest), &meta, digest, nil, false)
			if err != nil {
				return nil, err
			}
		}
		return &GenerateOutcome{Report: report, Digest: digest}, nil
	}
	return s.GenerateDraft(ctx, GenerateInput{ReportID: report.ID, Replace: in.Replace || created, Actor: in.Actor})
}

// hasManualTrace reports whether the report carries human-written content that
// an MCP-triggered regeneration must not overwrite without in-app approval.
func hasManualTrace(report *WeeklyReport) bool {
	if report.Meta.Generation == nil {
		return true
	}
	g := report.Meta.Generation
	return g.Mode == "manual" || g.AIStatus == "skipped"
}

func manualGeneration() GenerationMeta {
	return GenerationMeta{
		Mode:            "manual",
		AIStatus:        "skipped",
		TemplateID:      defaultReportTmpl,
		TemplateVersion: reportTemplateVer,
	}
}

func generationFrom(last output.GenerateResult, digest *ActivityDigest) GenerationMeta {
	meta := GenerationMeta{
		Mode:            "ai",
		AIStatus:        "ok",
		AIOutputID:      last.Output.ID,
		ProviderID:      last.Output.Provenance.ProviderID,
		ModelID:         last.Output.Provenance.ModelID,
		PromptVersion:   last.Output.Provenance.PromptVersion,
		TemplateID:      defaultReportTmpl,
		TemplateVersion: reportTemplateVer,
		GeneratedAt:     last.Output.Provenance.GeneratedAt,
		Attempts:        last.Attempts,
	}
	if meta.GeneratedAt.IsZero() {
		meta.GeneratedAt = last.Output.CreatedAt
	}
	if digest != nil {
		meta.DigestHash = digest.DigestHash
	}
	return meta
}

// storeDraft writes generated content and metadata in one short transaction,
// optionally emitting report.generated (actor=ai).
func (s *service) storeDraft(ctx context.Context, h interface {
	InTx(context.Context, func(*sql.Tx) error) error
	Events() events.Writer
}, reportID, content string, gen *GenerationMeta, digest *ActivityDigest, violations []Violation, emit bool) (*WeeklyReport, error) {
	if len(content) > maxContentBytes {
		return nil, serviceError(ErrContentTooLarge, "生成内容超过 512 KiB 上限")
	}
	now := s.clock.Now().UTC()
	err := h.InTx(ctx, func(tx *sql.Tx) error {
		report, err := s.reportTx(ctx, tx, reportID, false)
		if err != nil {
			return err
		}
		report.ContentMD = content
		report.UpdatedAt = now
		meta := report.Meta
		meta.Generation = gen
		meta.Coverage = coverageFrom(digest)
		meta.Refs = digestRefs(digest)
		if violations != nil {
			meta.Checks = &CheckState{CheckedAt: now, Violations: capViolations(violations)}
		}
		report.Meta = meta
		if err := updateReport(ctx, tx, report); err != nil {
			return err
		}
		if !emit {
			return nil
		}
		return h.Events().Emit(ctx, tx, events.Event{
			Type:       schema.EventReportGenerated,
			EntityType: schema.EntityWeeklyReport,
			EntityID:   report.ID,
			Summary:    fmt.Sprintf("生成周报 %s–%s（AI 初稿，prompt %s）", report.Period.StartDate(), report.Period.EndDate(), promptVersion),
			Actor:      schema.ActorAI,
			At:         s.clock.Now(),
		})
	})
	if err != nil {
		return nil, err
	}
	return s.getReport(ctx, reportID)
}

func digestSourceRefs(d *ActivityDigest, limit int) []output.SourceRef {
	seen := map[string]bool{}
	var out []output.SourceRef
	for _, sec := range d.Sections {
		for _, item := range sec.Items {
			key := item.Ref.Type + ":" + item.Ref.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, output.SourceRef{EntityType: item.Ref.Type, EntityID: item.Ref.ID})
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

func buildPromptMessages(info projectInfo, digest *ActivityDigest) []protocol.ChatMessage {
	var body strings.Builder
	body.WriteString("项目信息：\n")
	body.WriteString("- 名称：" + info.Name + "\n")
	body.WriteString("- 简介：" + info.Summary + "\n")
	body.WriteString("- 目标：" + info.Goals + "\n\n")
	body.WriteString("模板（default-7 v1）：\n")
	for _, sec := range defaultTemplate.Sections {
		fmt.Fprintf(&body, "%d. %s\n", sec.Num, sec.Title)
	}
	body.WriteString("\n活动汇总（唯一事实来源）：\n")
	raw, err := json.MarshalIndent(promptDigest(digest), "", "  ")
	if err == nil {
		body.Write(raw)
	}
	return []protocol.ChatMessage{
		{Role: protocol.RoleSystem, Content: promptSystem},
		{Role: protocol.RoleUser, Content: body.String()},
	}
}

const promptSystem = `你是科研周报助手。只输出 Markdown 正文，不要解释、不要 JSON 包装。
硬性约束：
1. 只能使用输入中的活动汇总、候选计划与项目元信息；不得补充外部知识或推测未提供的实验结果。
2. 输出必须恰好包含七个二级标题，顺序与模板一致：## 1. 本周工作内容 … ## 7. 下周计划。
3. 计划项（state=planned）只能出现在第 7 节，且只能来自 planned 候选；不得写成已完成。
4. 第 7 节不得写入已完成任务，不得虚构任务。
5. 引用未确认 AI 结论（unconfirmed_source=true）时必须标注「（待确认）」；无来源的成果性表述同样标注。
6. 不得编造数值；数值只能来自汇总中出现过的数字，不确定的度量用文字描述并标「（待确认）」。
7. 实验「失败/成功」不在输入范围内：只陈述记录的运行状态与 AI 分析原文，不得下结论。
8. 不得因实验未达预期而写 Idea 已否定；Idea 状态只能引用 status.changed 的新值。
9. 阻塞原因只能引用 blocked.reason 原文；当前该字段未落地，一律写「原因未记录」。
10. 无记录时写「本周期无记录」，不得填充泛化套话。每节不超过 12 条 bullet，单条不超过 120 字。
每条事实性 bullet 末尾附 [[ref:type:id]] 引用（可多个）。`

// promptDigest is the compact digest projection sent to the model.
func promptDigest(d *ActivityDigest) any {
	type item struct {
		Type              string `json:"type"`
		ID                string `json:"id"`
		Summary           string `json:"summary"`
		Detail            string `json:"detail,omitempty"`
		Old               string `json:"old,omitempty"`
		New               string `json:"new,omitempty"`
		State             string `json:"state"`
		UnconfirmedSource bool   `json:"unconfirmed_source"`
	}
	type section struct {
		Key   string `json:"key"`
		Title string `json:"title"`
		Items []item `json:"items"`
	}
	type blocked struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		Title  string `json:"title"`
		Column string `json:"column_name"`
		Reason string `json:"reason"`
	}
	type planned struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Title    string `json:"title"`
		Column   string `json:"column_name"`
		Origin   string `json:"origin"`
		Deadline string `json:"deadline,omitempty"`
		WeekPlan string `json:"week_plan,omitempty"`
	}
	out := struct {
		PeriodStart string    `json:"period_start"`
		PeriodEnd   string    `json:"period_end"`
		Sections    []section `json:"sections"`
		Blocked     []blocked `json:"blocked"`
		Planned     []planned `json:"planned"`
	}{
		PeriodStart: d.Period.StartDate(),
		PeriodEnd:   d.Period.EndDate(),
	}
	for _, sec := range d.Sections {
		cs := section{Key: string(sec.Key), Title: sec.Title}
		for _, it := range sec.Items {
			cs.Items = append(cs.Items, item{
				Type: it.Ref.Type, ID: it.Ref.ID, Summary: it.Summary, Detail: it.Detail,
				Old: it.Old, New: it.New, State: it.State, UnconfirmedSource: it.UnconfirmedSource,
			})
		}
		out.Sections = append(out.Sections, cs)
	}
	for _, b := range d.Blocked {
		out.Blocked = append(out.Blocked, blocked{
			Type: b.Ref.Type, ID: b.Ref.ID, Title: b.Title, Column: b.ColumnName, Reason: b.Reason,
		})
	}
	for _, p := range d.Planned {
		cp := planned{Type: p.Ref.Type, ID: p.Ref.ID, Title: p.Title, Column: p.ColumnName, Origin: p.Origin}
		if p.Deadline != nil {
			cp.Deadline = p.Deadline.Format(dateLayout)
		}
		if p.WeekPlan != nil {
			cp.WeekPlan = *p.WeekPlan
		}
		out.Planned = append(out.Planned, cp)
	}
	return out
}

// normalizeGenerated verifies the seven-section structure and re-renders it
// with canonical headings.
func normalizeGenerated(content string) (string, error) {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, "# ") {
		return "", errors.New("缺少一级标题")
	}
	lines := strings.Split(trimmed, "\n")
	parsed := parseReport(trimmed)
	seen := map[SectionKey][]string{}
	for _, key := range parsed.order {
		if _, dup := seen[key]; dup {
			return "", fmt.Errorf("章节重复：%s", key)
		}
		seen[key] = parsed.sections[key]
	}
	for _, sec := range defaultTemplate.Sections {
		if _, ok := seen[sec.Key]; !ok {
			return "", fmt.Errorf("缺少章节：%s", sec.Title)
		}
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(lines[0]))
	b.WriteString("\n\n")
	for i, sec := range defaultTemplate.Sections {
		fmt.Fprintf(&b, "## %d. %s\n", sec.Num, sec.Title)
		for _, line := range seen[sec.Key] {
			if strings.TrimSpace(line) == "" {
				continue
			}
			b.WriteString(strings.TrimRight(line, " \t"))
			b.WriteByte('\n')
		}
		if i != len(defaultTemplate.Sections)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String(), nil
}

// coverageFrom builds meta.coverage for a stored draft.
func coverageFrom(d *ActivityDigest) *Coverage {
	if d == nil {
		return nil
	}
	return &Coverage{
		PeriodStart:     d.Period.StartDate(),
		PeriodEnd:       d.Period.EndDate(),
		Timezone:        d.Timezone,
		EventCount:      totalEvents(d.Stats.CountsByType),
		CountsByType:    d.Stats.CountsByType,
		CountsBySection: sectionCounts(d.Stats.CountsBySection),
		Truncated:       d.Truncated,
		DigestHash:      d.DigestHash,
		ComputedAt:      d.CollectedAt,
	}
}

func totalEvents(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func sectionCounts(m map[SectionKey]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[string(k)] = v
	}
	return out
}

func digestRefs(d *ActivityDigest) []EntityRef {
	seen := map[string]bool{}
	var out []EntityRef
	for _, sec := range d.Sections {
		for _, item := range sec.Items {
			key := item.Ref.Type + ":" + item.Ref.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, item.Ref)
			if len(out) >= maxRefs {
				return out
			}
		}
	}
	return out
}
