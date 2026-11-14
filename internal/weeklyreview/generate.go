package weeklyreview

import (
	"context"
	"database/sql"
	"encoding/json"
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

	run := s.generateSections(ctx, info, digest, report.ID, in.Progress)

	if len(run.ids) == 0 {
		// Nothing usable came back: keep the current body, or write the
		// deterministic skeleton, and say why.
		meta := manualGeneration()
		meta.AIStatus = "failed"
		meta.Attempts = run.attempts
		if ctx.Err() != nil {
			meta.Error = ctx.Err().Error()
		} else if run.firstErr != nil {
			meta.Error = run.firstErr.Error()
		}
		content := report.ContentMD
		if strings.TrimSpace(content) == "" {
			content = BuildSkeleton(info.Name, report.Period, digest)
		}
		updated, uerr := s.storeDraft(ctx, h, report.ID, content, &meta, digest, nil, false)
		if uerr != nil {
			return outcome, uerr
		}
		outcome.Report = updated
		if ctx.Err() != nil {
			return outcome, ctx.Err()
		}
		return outcome, serviceError(ErrAIUnavailable, "AI 生成失败：%v", run.firstErr)
	}

	parsed := assembleReport(info, report.Period, run.lines)
	violations := Validate(parsed, defaultTemplate, digest)
	meta := generationFrom(run.last, digest)
	meta.Attempts = run.attempts
	meta.AIStatus = "ok"
	meta.AIOutputIDs = run.ids
	meta.AIOutputID = run.ids[0]
	if len(run.failed) > 0 {
		meta.Notes = partialNote(run.failed, run.firstErr)
	}
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
	return s.GenerateDraft(ctx, GenerateInput{
		ReportID: report.ID, Replace: in.Replace || created, Actor: in.Actor, Progress: in.Progress,
	})
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
		// A draft always carries the checks of the body that was just
		// written, including the deterministic fallbacks. Without this the
		// UI reads a nil/stale check state and claims "all acknowledged"
		// while Confirm re-validates and rejects the same body.
		checks := violations
		if checks == nil {
			checks = mergeAcks(Validate(content, defaultTemplate, digest), report.Meta.Checks)
		}
		meta.Checks = buildCheckState(checks, now)
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

// sectionAttempts is how many calls one section gets before the
// deterministic skeleton stands in for it.
const sectionAttempts = 2

// sectionMaxTokens bounds one section. Each call is small enough to finish
// well inside the chat timeout, which is why generation is split at all.
const sectionMaxTokens = 900

// sectionHints tell the model what each section is for.
var sectionHints = map[SectionKey]string{
	SectionWork:       "先用一至两句话总述本周主线：推进了什么、推进到哪一步、卡在哪里；其下按主题分条，合并同一实体的多条记录。",
	SectionLiterature: "写读文献得到的要点以及它与本课题的关系，不写「导入/收藏了文献」这类动作。",
	SectionTheory:     "写理论推导、方法或模型上的推进与结论。",
	SectionExperiment: "写实验与运行的结果和当前状态；不下「成功/失败」结论。",
	SectionIdea:       "写 Idea 的状态变化、证据与取舍，不写「创建了 Idea」。",
	SectionProblem:    "写受阻项、未解决的矛盾与需要决策的点；原因只引用原文。",
	SectionNextWeek:   "只列下周要做的候选任务，每条写明依据（周计划 / 截止日 / 顺延）。",
}

// sectionRun is the outcome of generating every section separately.
type sectionRun struct {
	lines    map[SectionKey][]string
	ids      []string
	failed   []SectionKey
	attempts int
	last     output.GenerateResult
	firstErr error
}

// generateSections renders the seven sections one model call at a time. One
// large call times out; seven small ones each finish in seconds, a failure
// costs one section instead of the report, and the reader sees progress.
func (s *service) generateSections(ctx context.Context, info projectInfo, digest *ActivityDigest, reportID string, progress SectionProgress) sectionRun {
	run := sectionRun{lines: make(map[SectionKey][]string, len(defaultTemplate.Sections))}
	entity := &output.EntityRef{Type: string(schema.EntityWeeklyReport), ID: reportID}
	total := len(defaultTemplate.Sections)
	for i, sec := range defaultTemplate.Sections {
		if progress != nil {
			progress(i, total, sec.Key)
		}
		if sectionIsEmpty(digest, sec.Key) {
			// Nothing to write from: the deterministic line is both faster
			// and safer than letting the model invent content.
			run.lines[sec.Key] = []string{"- 本周期无记录"}
			continue
		}
		req := output.GenerateRequest{
			ProjectID:       s.projectID,
			TaskKind:        taskKind,
			PromptVersion:   promptVersion,
			Messages:        sectionMessages(info, digest, sec),
			SourceRefs:      sectionSourceRefs(digest, sec.Key),
			Entity:          entity,
			MaxOutputTokens: sectionMaxTokens,
		}
		var got []string
		for try := 0; try < sectionAttempts; try++ {
			res, err := s.ai.Generate(ctx, req)
			if err != nil {
				run.attempts++
				if run.firstErr == nil {
					run.firstErr = err
				}
				if ctx.Err() != nil {
					return run
				}
				continue
			}
			run.attempts += max(res.Attempts, 1)
			lines, perr := normalizeSectionOutput(res.Output.ContentMD, sec)
			if perr != nil {
				if run.firstErr == nil {
					run.firstErr = fmt.Errorf("%s：%w", sec.Title, perr)
				}
				continue
			}
			got, run.last = lines, res
			run.ids = append(run.ids, res.Output.ID)
			break
		}
		if got == nil {
			// This section alone falls back to digest facts.
			got = skeletonLines(sec.Key, digest.Period, digest)
			if len(got) == 0 {
				got = []string{"- 本周期无记录"}
			}
			run.failed = append(run.failed, sec.Key)
		}
		run.lines[sec.Key] = got
	}
	if progress != nil {
		progress(total, total, "")
	}
	return run
}

// sectionIsEmpty reports whether a section has no facts to write from. The
// work section always runs: it carries the week's overview.
func sectionIsEmpty(d *ActivityDigest, key SectionKey) bool {
	if key == SectionWork {
		return false
	}
	if len(sectionItems(d, key)) > 0 {
		return false
	}
	switch key {
	case SectionProblem:
		return len(d.Blocked) == 0
	case SectionNextWeek:
		return len(d.Planned) == 0
	}
	return true
}

// sectionMessages is the prompt for exactly one section.
func sectionMessages(info projectInfo, digest *ActivityDigest, sec TemplateSection) []protocol.ChatMessage {
	var body strings.Builder
	body.WriteString("项目：" + info.Name + "\n")
	if info.Summary != "" {
		body.WriteString("简介：" + info.Summary + "\n")
	}
	if info.Goals != "" {
		body.WriteString("目标：" + info.Goals + "\n")
	}
	fmt.Fprintf(&body, "\n周期：%s – %s\n", digest.Period.StartDate(), digest.Period.EndDate())
	fmt.Fprintf(&body, "本次只写第 %d 节「%s」。\n", sec.Num, sec.Title)
	if hint := sectionHints[sec.Key]; hint != "" {
		body.WriteString("本节要求：" + hint + "\n")
	}
	if sec.Key == SectionWork {
		if overview := sectionOverview(digest); len(overview) > 0 {
			body.WriteString("\n本周各分区概览（仅供总述，不要在正文里重复它们的细节）：\n")
			if raw, err := json.MarshalIndent(overview, "", "  "); err == nil {
				body.Write(raw)
				body.WriteString("\n")
			}
		}
	}
	if sec.Key == SectionWork && len(digest.Housekeeping) > 0 {
		fmt.Fprintf(&body, "另有 %d 条创建/导入/删除等记录，属于事务性操作，不要写入正文。\n", len(digest.Housekeeping))
	}
	body.WriteString("\n本节活动条目（唯一事实来源）：\n")
	if raw, err := json.MarshalIndent(sectionPromptDigest(digest, sec.Key), "", "  "); err == nil {
		body.Write(raw)
	}
	return []protocol.ChatMessage{
		{Role: protocol.RoleSystem, Content: promptSectionSystem},
		{Role: protocol.RoleUser, Content: body.String()},
	}
}

// sectionPromptDigest scopes the digest to the one section being written.
func sectionPromptDigest(d *ActivityDigest, key SectionKey) any {
	scoped := &ActivityDigest{
		Period:   d.Period,
		Timezone: d.Timezone,
		Sections: []DigestSection{{Key: key, Title: sectionTitle(key), Items: sectionItems(d, key)}},
	}
	switch key {
	case SectionProblem:
		scoped.Blocked = d.Blocked
	case SectionNextWeek:
		scoped.Planned = d.Planned
	}
	return promptDigest(scoped)
}

// sectionOverview is the per-section headline list the work section reads so
// its two-sentence summary can name the week's real threads.
func sectionOverview(d *ActivityDigest) map[string][]string {
	out := map[string][]string{}
	for _, sec := range d.Sections {
		if sec.Key == SectionWork {
			continue
		}
		var lines []string
		for i, item := range sec.Items {
			if i >= 3 {
				break
			}
			lines = append(lines, item.Summary)
		}
		if len(lines) > 0 {
			out[sec.Title] = lines
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sectionSourceRefs are the entities one section may cite.
func sectionSourceRefs(d *ActivityDigest, key SectionKey) []output.SourceRef {
	seen := map[string]bool{}
	var out []output.SourceRef
	add := func(ref EntityRef) {
		if ref.Type == "" || ref.ID == "" || seen[ref.Type+":"+ref.ID] {
			return
		}
		seen[ref.Type+":"+ref.ID] = true
		out = append(out, output.SourceRef{EntityType: ref.Type, EntityID: ref.ID})
	}
	for _, item := range sectionItems(d, key) {
		add(item.Ref)
	}
	if key == SectionProblem {
		for _, task := range d.Blocked {
			add(task.Ref)
		}
	}
	if key == SectionNextWeek {
		for _, task := range d.Planned {
			add(task.Ref)
		}
	}
	return out
}

// normalizeSectionOutput turns one model answer into the body lines of a
// section: fences and headings are dropped (assembly owns the structure) and
// an empty answer is an error, not a silent blank.
func normalizeSectionOutput(content string, sec TemplateSection) ([]string, error) {
	text := strings.TrimSpace(content)
	if strings.HasPrefix(text, "```") {
		if nl := strings.IndexByte(text, '\n'); nl >= 0 {
			text = text[nl+1:]
		}
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") {
			// A model may repeat the heading; assembly writes it.
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			continue
		}
		out = append(out, strings.TrimRight(line, " \t"))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s 分区返回空内容", sec.Title)
	}
	return out, nil
}

// assembleReport writes the seven canonical headings around the generated
// section bodies.
func assembleReport(info projectInfo, p Period, sections map[SectionKey][]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Weekly Research Report — %s（%s – %s）\n\n", info.Name, p.StartDate(), p.EndDate())
	tpl := defaultTemplate.Sections
	for i, sec := range tpl {
		fmt.Fprintf(&b, "## %d. %s\n", sec.Num, sec.Title)
		lines := sections[sec.Key]
		if len(lines) == 0 {
			lines = []string{"- 本周期无记录"}
		}
		for _, line := range lines {
			b.WriteString(line)
			b.WriteByte('\n')
		}
		if i != len(tpl)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// partialNote explains which sections fell back to the skeleton.
func partialNote(failed []SectionKey, cause error) string {
	titles := make([]string, 0, len(failed))
	for _, key := range failed {
		titles = append(titles, sectionTitle(key))
	}
	note := "以下分区未由 AI 生成，已写入确定性骨架：" + strings.Join(titles, "、")
	if cause != nil {
		note += "（" + truncateRunes(cause.Error(), 160) + "）"
	}
	return note
}

// promptSectionSystem is the shared contract of a single-section call.
const promptSectionSystem = `你是科研周报撰写助手。本次只写七个分区中的**一个**分区，只输出该分区的正文（Markdown 列表或短段落），不要输出任何标题、不要解释、不要 JSON 包装。

读者是导师和合作者：他们要看到这一周的实质进展与价值，不是文件操作日志。
1. 只写有信息量的工作：完成了什么、推进到哪一步、得到什么结果、遇到什么阻碍。
2. 不要写「创建项目」「导入文献」「创建笔记」「新建任务」「导出文件」这类记录性动作——它们不是进展；输入里即使出现也不要写。
3. 同一实体或同一主题的多条记录合并成一条「做了什么 → 现在什么状态」，不要按时间逐条复述。
4. 输出 2–6 条；每条不超过 120 字；每条事实性条目末尾附 [[ref:type:id]] 引用（可多个）。
5. 只能使用输入的活动条目、候选计划与项目元信息；不得补充外部知识，不得编造数值、结论或实验结果。
6. 引用 unconfirmed_source=true 的内容必须标注「（待确认）」；无来源的成果性表述同样标注。
7. 实验不得下「成功/失败」结论；Idea 状态只能引用 status.changed 的新值。
8. 本节没有可写的实质内容时，只输出一行「本周期无记录」。`

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
