package weeklyreview

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"researchos/internal/foundation/events"
	"researchos/internal/foundation/schema"
)

// CollectActivities builds the deterministic digest of one period
// (SPEC-09 §5.3). It performs read-only queries and writes nothing.
func (s *service) CollectActivities(ctx context.Context, in CollectInput) (*ActivityDigest, error) {
	cfg := s.loadSettings(ctx)
	loc := cfg.Location
	if in.Timezone != nil {
		loc = in.Timezone
	}
	period := in.Period
	if period.Empty() {
		def, err := s.DefaultPeriod(ctx)
		if err != nil {
			return nil, err
		}
		period = def
	}
	if err := validatePeriod(period, loc); err != nil {
		return nil, err
	}
	maxEvents := in.MaxEvents
	if maxEvents <= 0 {
		maxEvents = cfg.MaxEvents
	}
	h, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}

	// events.Query.Until is inclusive (created_at <= Until) while the report
	// window is [Start, EndExclusive). Subtracting one nanosecond is the
	// narrowest equivalent exclusive bound (SPEC-09 §2.3, O17).
	since := period.Start
	until := period.EndExclusive.Add(-time.Nanosecond)
	var all []events.Activity
	for offset := 0; ; {
		page, err := h.Reader().List(ctx, events.Query{
			Since:  &since,
			Until:  &until,
			Limit:  500,
			Offset: offset,
		})
		if err != nil {
			return nil, fmt.Errorf("weeklyreview: collect activities: %w", err)
		}
		all = append(all, page...)
		if len(page) < 500 {
			break
		}
		offset += len(page)
	}

	// CountsByType/ByActor reflect the whole window; truncation only affects
	// the rendered entries.
	countsByType := map[string]int{}
	countsByActor := map[string]int{}
	for _, ev := range all {
		countsByType[string(ev.Type)]++
		countsByActor[string(ev.Actor)]++
	}

	truncated := false
	kept := all
	if maxEvents > 0 && len(kept) > maxEvents {
		kept = truncateEvents(kept, maxEvents)
		truncated = true
	}
	// events.Reader.List returns newest first; sections are rendered in
	// ascending time order (SPEC-09 §7.3).
	sort.SliceStable(kept, func(i, j int) bool {
		if !kept[i].CreatedAt.Equal(kept[j].CreatedAt) {
			return kept[i].CreatedAt.Before(kept[j].CreatedAt)
		}
		return kept[i].ID < kept[j].ID
	})

	refs := collectRefs(kept)
	briefs, err := s.resolveBriefs(ctx, refs)
	if err != nil {
		return nil, err
	}

	var warnings []string
	warn := func(msg string) {
		for _, existing := range warnings {
			if existing == msg {
				return
			}
		}
		warnings = append(warnings, msg)
	}

	sections := make(map[SectionKey][]DigestItem)
	for _, ev := range kept {
		item := digestItem(ev, briefs, loc, warn)
		sections[item.section] = append(sections[item.section], item.item)
	}

	blocked, err := s.blockedTasks(ctx)
	if err != nil {
		return nil, err
	}
	nextISO := isoWeek(period.EndExclusive, loc)
	currentISO := isoWeek(period.EndExclusive.AddDate(0, 0, -1), loc)
	planned, err := s.plannedTasks(ctx, period, nextISO, currentISO, loc)
	if err != nil {
		return nil, err
	}

	digest := &ActivityDigest{
		Period:      period,
		Timezone:    loc.String(),
		CollectedAt: s.clock.Now(),
		Blocked:     blocked,
		Planned:     planned,
		Truncated:   truncated,
		Warnings:    warnings,
		Stats: DigestStats{
			CountsByType:    countsByType,
			CountsBySection: map[SectionKey]int{},
			CountsByActor:   countsByActor,
		},
	}

	for _, sec := range defaultTemplate.Sections {
		items := sections[sec.Key]
		items = collapseStatuses(items)
		if len(items) > maxSectionItems {
			items = items[:maxSectionItems]
		}
		if items == nil {
			items = []DigestItem{}
		}
		digest.Sections = append(digest.Sections, DigestSection{Key: sec.Key, Title: sec.Title, Items: items})
		digest.Stats.CountsBySection[sec.Key] = len(items)
	}

	if cfg.WeekStartDay != 1 {
		warn(fmt.Sprintf("周报周期起点为周%d，与 ISO 周（周一起算）不对齐；下周计划按 ISO 周 %s 取候选",
			cfg.WeekStartDay, nextISO))
		digest.Warnings = warnings
	}
	if truncated {
		digest.Warnings = append(digest.Warnings, fmt.Sprintf("活动条目超过上限 %d，已按优先级截断", maxEvents))
	}

	hash, err := digestCanonicalHash(digest)
	if err != nil {
		return nil, err
	}
	digest.DigestHash = hash
	return digest, nil
}

// eventPriority orders events for truncation (higher is kept first).
func eventPriority(ev events.Activity) int {
	switch ev.Type {
	case schema.EventStatusChanged:
		return 60
	case schema.EventAIAnalysisCompleted:
		return 50
	case schema.EventPaperImported, schema.EventPaperOverwriteConfirmed:
		return 40
	case schema.EventEntityCreated:
		return 30
	case schema.EventRelationAdded, schema.EventRelationRemoved:
		return 20
	case schema.EventNoteUpdated:
		return 10
	default:
		return 5
	}
}

func truncateEvents(all []events.Activity, max int) []events.Activity {
	sorted := append([]events.Activity(nil), all...)
	sort.SliceStable(sorted, func(i, j int) bool {
		pi, pj := eventPriority(sorted[i]), eventPriority(sorted[j])
		if pi != pj {
			return pi > pj
		}
		if !sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
		}
		return sorted[i].ID > sorted[j].ID
	})
	kept := sorted[:max]
	sort.SliceStable(kept, func(i, j int) bool {
		if !kept[i].CreatedAt.Equal(kept[j].CreatedAt) {
			return kept[i].CreatedAt.Before(kept[j].CreatedAt)
		}
		return kept[i].ID < kept[j].ID
	})
	return kept
}

type digestItemResult struct {
	section SectionKey
	item    DigestItem
}

func digestItem(ev events.Activity, briefs map[EntityRef]EntityBrief, loc *time.Location, warn func(string)) digestItemResult {
	ref := EntityRef{Type: string(ev.EntityType), ID: ev.EntityID}
	brief := briefs[ref]
	section, warning := classify(ev, brief)
	if warning != "" {
		warn(warning)
	}

	summary := strings.TrimSpace(ev.Payload.Summary)
	if summary == "" && ev.Type == schema.EventStatusChanged && ev.EntityType == schema.EntityTask {
		summary = taskChangeSummary(ev.Payload.Old, ev.Payload.New, brief.Title)
	}
	if summary == "" {
		summary = defaultSummary(ev)
	}
	if summary == "" {
		summary = brief.Title
	}
	if summary == "" {
		summary = string(ev.EntityType) + " " + ev.EntityID
	}
	summary = truncateRunes(summary, maxSummaryRunes)

	kind := itemKind(ev.Type)
	oldText, newText := normalizeStatus(ev, brief)
	detail := ""
	firstAt := ev.CreatedAt.In(loc)
	lastAt := firstAt
	if ev.Type == schema.EventNoteUpdated {
		n := ev.Payload.SaveCount
		if n <= 0 {
			n = 1
		}
		detail = fmt.Sprintf("%d 次保存", n)
		if ev.Payload.LastSavedAt != "" {
			if t, err := time.Parse(time.RFC3339, ev.Payload.LastSavedAt); err == nil {
				lastAt = t.In(loc)
			}
		}
	}
	if ev.Type == schema.EventRelationAdded || ev.Type == schema.EventRelationRemoved {
		name := "关联"
		if ev.Type == schema.EventRelationRemoved {
			name = "解除关联"
		}
		detail = relationText(ev, name)
	}
	if brief.AIContentUnconfirmed {
		if detail == "" {
			detail = "AI 内容待确认"
		} else {
			detail += "；AI 内容待确认"
		}
	}
	if brief.Broken {
		summary += "（目标已删除）"
	}

	return digestItemResult{
		section: section,
		item: DigestItem{
			Ref:               ref,
			Kind:              kind,
			State:             "done",
			Summary:           summary,
			Detail:            detail,
			Old:               oldText,
			New:               newText,
			Actor:             string(ev.Actor),
			FirstAt:           firstAt,
			LastAt:            lastAt,
			EventIDs:          []string{ev.ID},
			UnconfirmedSource: brief.AIContentUnconfirmed,
			Broken:            brief.Broken,
		},
	}
}

func itemKind(t events.Type) string {
	switch t {
	case schema.EventEntityCreated:
		return "created"
	case schema.EventEntityDeleted:
		return "deleted"
	case schema.EventEntityRestored:
		return "restored"
	case schema.EventStatusChanged:
		return "status"
	case schema.EventNoteUpdated, schema.EventEntityUpdated:
		return "updated"
	case schema.EventAIAnalysisCompleted:
		return "ai_done"
	case schema.EventRelationAdded, schema.EventRelationRemoved:
		return "relation"
	case schema.EventPaperImported, schema.EventPaperOverwriteConfirmed, schema.EventPaperMetadataUpdated:
		return "import"
	case schema.EventReportGenerated:
		return "report"
	case schema.EventProjectExported:
		return "export"
	default:
		return "state"
	}
}

func defaultSummary(ev events.Activity) string {
	switch ev.Type {
	case schema.EventEntityCreated:
		return "创建 " + string(ev.EntityType)
	case schema.EventEntityDeleted:
		return "移入回收站"
	case schema.EventEntityRestored:
		return "已从回收站恢复"
	case schema.EventStatusChanged:
		return "状态变更"
	case schema.EventNoteUpdated:
		return "更新笔记"
	case schema.EventAIAnalysisCompleted:
		return "AI 分析完成"
	case schema.EventRelationAdded:
		return "新增关联"
	case schema.EventRelationRemoved:
		return "解除关联"
	default:
		return ""
	}
}

// classify maps one event to a section and an optional warning (SPEC-09 §7.3).
func classify(ev events.Activity, brief EntityBrief) (SectionKey, string) {
	entity := string(ev.EntityType)
	switch ev.Type {
	case schema.EventRelationAdded, schema.EventRelationRemoved:
		return relationSection(ev)
	case schema.EventPaperImported, schema.EventPaperOverwriteConfirmed, schema.EventPaperMetadataUpdated:
		return SectionLiterature, ""
	case schema.EventReportGenerated, schema.EventProjectExported:
		return SectionWork, ""
	case schema.EventAIAnalysisCompleted:
		switch entity {
		case string(schema.EntitySnapshot), string(schema.EntityExperiment), string(schema.EntityRun):
			return SectionExperiment, ""
		case string(schema.EntityIdea):
			// SPEC-06 emits ai.analysis_completed on the idea entity (§2.3).
			return SectionIdea, ""
		case string(schema.EntityWeeklyReport):
			return SectionWork, ""
		default:
			return classifyEntity(entity, brief)
		}
	case schema.EventStatusChanged:
		if entity == string(schema.EntityTask) {
			return taskSection(ev.Payload.Old, ev.Payload.New)
		}
		return classifyEntity(entity, brief)
	default:
		return classifyEntity(entity, brief)
	}
}

func relationSection(ev events.Activity) (SectionKey, string) {
	obj := ev.Payload.New
	if ev.Type == schema.EventRelationRemoved {
		obj = ev.Payload.Old
	}
	if obj == nil {
		return SectionWork, ""
	}
	src, _ := (*obj)["src_type"].(string)
	dst, _ := (*obj)["dst_type"].(string)
	switch {
	case src == string(schema.EntityIdea) || dst == string(schema.EntityIdea):
		return SectionIdea, ""
	case src == string(schema.EntityExperiment) || dst == string(schema.EntityExperiment),
		src == string(schema.EntityRun) || dst == string(schema.EntityRun):
		return SectionExperiment, ""
	case (src == string(schema.EntityNote) && dst == string(schema.EntityPaper)),
		(src == string(schema.EntityPaper) && dst == string(schema.EntityNote)):
		return SectionLiterature, ""
	default:
		return SectionWork, ""
	}
}

// taskSection maps a task status.changed event. The payload old/new objects
// carry the full SPEC-08 task shape; a blocked transition belongs to
// 「遇到的问题」, everything else (column move, completion, archive) to
// 「本周工作内容」. Archive classification is still unconfirmed (SPEC-08 N10),
// so it stays in work and adds a warning.
func taskSection(oldObj, newObj *events.Object) (SectionKey, string) {
	_, _, _, oldBlocked, _ := taskShape(oldObj)
	_, _, _, newBlocked, newArchived := taskShape(newObj)
	if newArchived != nil && *newArchived {
		return SectionWork, "任务归档事件的归类待确认，暂归「工作内容」"
	}
	if newBlocked != nil {
		if *newBlocked && (oldBlocked == nil || !*oldBlocked) {
			return SectionProblem, ""
		}
		if !*newBlocked && oldBlocked != nil && *oldBlocked {
			return SectionProblem, ""
		}
	}
	return SectionWork, ""
}

// taskChangeSummary renders a SPEC-08 status.changed object as readable text.
// It is used when the event carries no summary so the digest never surfaces
// raw JSON (SPEC-09 §2.3).
func taskChangeSummary(oldObj, newObj *events.Object, title string) string {
	if title == "" {
		title = "任务"
	}
	newColumn, newDone, _, newBlocked, newArchived := taskShape(newObj)
	oldColumn, _, _, oldBlocked, _ := taskShape(oldObj)
	switch {
	case newArchived != nil && *newArchived:
		return "归档任务：" + title
	case newBlocked != nil && ((*newBlocked && (oldBlocked == nil || !*oldBlocked)) ||
		(!*newBlocked && oldBlocked != nil && *oldBlocked)):
		if *newBlocked {
			return title + " 标记受阻"
		}
		return title + " 解除受阻"
	case newDone && newColumn != "":
		return fmt.Sprintf("完成任务：%s（进入 %s）", title, newColumn)
	case newColumn != "" && newColumn != oldColumn && oldColumn != "":
		return fmt.Sprintf("%s：%s → %s", title, oldColumn, newColumn)
	}
	return ""
}

func classifyEntity(entity string, brief EntityBrief) (SectionKey, string) {
	switch schema.EntityType(entity) {
	case schema.EntityNote:
		switch schema.NoteKind(brief.NoteKind) {
		case schema.NotePaperNote, schema.NoteOverview:
			return SectionLiterature, ""
		case schema.NoteScratchpad:
			return SectionTheory, ""
		case schema.NoteDBRecord:
			return SectionTheory, ""
		default:
			return SectionTheory, ""
		}
	case schema.EntityDBRecord:
		return SectionTheory, ""
	case schema.EntityPaper:
		return SectionLiterature, ""
	case schema.EntityIdea, schema.EntityEvidence:
		return SectionIdea, ""
	case schema.EntityExperiment, schema.EntityRun, schema.EntitySnapshot:
		return SectionExperiment, ""
	case schema.EntityTask:
		return SectionWork, ""
	case schema.EntityWeeklyReport, schema.EntityProject:
		return SectionWork, ""
	default:
		return SectionWork, fmt.Sprintf("未识别的实体类型 %q，已归入「工作内容」", entity)
	}
}

// taskShape extracts the SPEC-08 status.changed object keys.
func taskShape(obj *events.Object) (column string, isDone, hasIsDone bool, blocked, archived *bool) {
	if obj == nil {
		return "", false, false, nil, nil
	}
	if v, ok := (*obj)["column_name"].(string); ok && v != "" {
		column = v
	} else if v, ok := (*obj)["column"].(string); ok {
		column = v
	}
	if v, ok := (*obj)["is_done"].(bool); ok {
		isDone, hasIsDone = v, true
	}
	if v, ok := (*obj)["blocked"].(bool); ok {
		blocked = &v
	}
	if v, ok := (*obj)["archived"].(bool); ok {
		archived = &v
	}
	return column, isDone, hasIsDone, blocked, archived
}

func normalizeStatus(ev events.Activity, brief EntityBrief) (string, string) {
	if ev.Type != schema.EventStatusChanged && ev.Type != schema.EventPaperMetadataUpdated {
		return "", ""
	}
	if ev.EntityType == schema.EntityTask {
		oldText := taskStatusText(ev.Payload.Old)
		newText := taskStatusText(ev.Payload.New)
		return oldText, newText
	}
	oldText := objectStatus(ev.Payload.Old)
	newText := objectStatus(ev.Payload.New)
	return oldText, newText
}

func objectStatus(obj *events.Object) string {
	if obj == nil {
		return ""
	}
	if v, ok := (*obj)["status"].(string); ok {
		return v
	}
	return ""
}

func taskStatusText(obj *events.Object) string {
	column, isDone, hasIsDone, blocked, archived := taskShape(obj)
	var parts []string
	if column != "" {
		parts = append(parts, "列："+column)
	}
	if hasIsDone {
		if isDone {
			parts = append(parts, "已完成")
		} else {
			parts = append(parts, "未完成")
		}
	}
	if blocked != nil {
		if *blocked {
			parts = append(parts, "受阻")
		} else {
			parts = append(parts, "未受阻")
		}
	}
	if archived != nil {
		if *archived {
			parts = append(parts, "已归档")
		} else {
			parts = append(parts, "未归档")
		}
	}
	return strings.Join(parts, "，")
}

// collapseStatuses keeps at most three consecutive status transitions per
// entity and folds the rest into one deterministic summary item, preserving
// the position of the first dropped transition.
func collapseStatuses(items []DigestItem) []DigestItem {
	const maxRun = 3
	out := make([]DigestItem, 0, len(items))
	for i := 0; i < len(items); {
		if items[i].Kind != "status" {
			out = append(out, items[i])
			i++
			continue
		}
		j := i
		for j < len(items) && items[j].Kind == "status" && items[j].Ref == items[i].Ref {
			j++
		}
		run := items[i:j]
		if len(run) <= maxRun {
			out = append(out, run...)
			i = j
			continue
		}
		out = append(out, run[:maxRun]...)
		folded := run[maxRun]
		folded.Summary = truncateRunes("[状态变更折叠] "+folded.Summary, maxSummaryRunes)
		folded.Detail = fmt.Sprintf("另有 %d 次状态变更", len(run)-maxRun)
		folded.EventIDs = nil
		for _, dropped := range run[maxRun:] {
			folded.EventIDs = append(folded.EventIDs, dropped.EventIDs...)
		}
		folded.LastAt = run[len(run)-1].LastAt
		out = append(out, folded)
		i = j
	}
	return out
}

// relationText renders one relation tuple for display.
func relationText(ev events.Activity, prefix string) string {
	obj := ev.Payload.New
	if ev.Type == schema.EventRelationRemoved {
		obj = ev.Payload.Old
	}
	if obj == nil {
		return prefix
	}
	srcType, _ := (*obj)["src_type"].(string)
	srcID, _ := (*obj)["src_id"].(string)
	rel, _ := (*obj)["relation"].(string)
	dstType, _ := (*obj)["dst_type"].(string)
	dstID, _ := (*obj)["dst_id"].(string)
	return fmt.Sprintf("%s：%s/%s —%s→ %s/%s", prefix, srcType, srcID, rel, dstType, dstID)
}

func collectRefs(list []events.Activity) []EntityRef {
	seen := map[EntityRef]bool{}
	var out []EntityRef
	add := func(ref EntityRef) {
		if ref.Type == "" || ref.ID == "" || seen[ref] {
			return
		}
		seen[ref] = true
		out = append(out, ref)
	}
	for _, ev := range list {
		add(EntityRef{Type: string(ev.EntityType), ID: ev.EntityID})
		if ev.Type == schema.EventRelationAdded || ev.Type == schema.EventRelationRemoved {
			obj := ev.Payload.New
			if obj == nil {
				obj = ev.Payload.Old
			}
			if obj != nil {
				srcType, _ := (*obj)["src_type"].(string)
				srcID, _ := (*obj)["src_id"].(string)
				dstType, _ := (*obj)["dst_type"].(string)
				dstID, _ := (*obj)["dst_id"].(string)
				add(EntityRef{Type: srcType, ID: srcID})
				add(EntityRef{Type: dstType, ID: dstID})
			}
		}
		if ev.Type == schema.EventAIAnalysisCompleted {
			if ev.Payload.New != nil {
				if t, ok := (*ev.Payload.New)["target_type"].(string); ok {
					if id, ok := (*ev.Payload.New)["target_id"].(string); ok {
						add(EntityRef{Type: t, ID: id})
					}
				}
			}
		}
	}
	return out
}

func digestCanonicalHash(d *ActivityDigest) (string, error) {
	type canonicalItem struct {
		Type        string   `json:"type"`
		ID          string   `json:"id"`
		Kind        string   `json:"kind"`
		State       string   `json:"state"`
		Summary     string   `json:"summary"`
		Detail      string   `json:"detail"`
		Old         string   `json:"old"`
		New         string   `json:"new"`
		Actor       string   `json:"actor"`
		FirstAt     string   `json:"first_at"`
		LastAt      string   `json:"last_at"`
		EventIDs    []string `json:"event_ids"`
		Unconfirmed bool     `json:"unconfirmed_source"`
		Broken      bool     `json:"broken"`
	}
	type canonicalSection struct {
		Key   string          `json:"key"`
		Items []canonicalItem `json:"items"`
	}
	type canonicalBlocked struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		Title  string `json:"title"`
		Column string `json:"column_name"`
		IsDone bool   `json:"is_done"`
		Reason string `json:"reason"`
	}
	type canonicalPlanned struct {
		Type     string `json:"type"`
		ID       string `json:"id"`
		Title    string `json:"title"`
		Column   string `json:"column_name"`
		IsDone   bool   `json:"is_done"`
		Deadline string `json:"deadline,omitempty"`
		WeekPlan string `json:"week_plan,omitempty"`
		Origin   string `json:"origin"`
	}
	canon := struct {
		Start     string             `json:"period_start"`
		End       string             `json:"period_end"`
		Timezone  string             `json:"timezone"`
		Sections  []canonicalSection `json:"sections"`
		Blocked   []canonicalBlocked `json:"blocked"`
		Planned   []canonicalPlanned `json:"planned"`
		Stats     map[string]int     `json:"counts_by_type"`
		Truncated bool               `json:"truncated"`
		Warnings  []string           `json:"warnings"`
	}{
		Start:     d.Period.StartDate(),
		End:       d.Period.EndDate(),
		Timezone:  d.Timezone,
		Stats:     d.Stats.CountsByType,
		Truncated: d.Truncated,
		Warnings:  d.Warnings,
	}
	for _, sec := range d.Sections {
		cs := canonicalSection{Key: string(sec.Key)}
		for _, item := range sec.Items {
			cs.Items = append(cs.Items, canonicalItem{
				Type: item.Ref.Type, ID: item.Ref.ID, Kind: item.Kind, State: item.State,
				Summary: item.Summary, Detail: item.Detail, Old: item.Old, New: item.New,
				Actor: item.Actor, FirstAt: item.FirstAt.Format(time.RFC3339),
				LastAt: item.LastAt.Format(time.RFC3339), EventIDs: item.EventIDs,
				Unconfirmed: item.UnconfirmedSource, Broken: item.Broken,
			})
		}
		canon.Sections = append(canon.Sections, cs)
	}
	for _, b := range d.Blocked {
		canon.Blocked = append(canon.Blocked, canonicalBlocked{
			Type: b.Ref.Type, ID: b.Ref.ID, Title: b.Title, Column: b.ColumnName,
			IsDone: b.IsDone, Reason: b.Reason,
		})
	}
	for _, p := range d.Planned {
		cp := canonicalPlanned{
			Type: p.Ref.Type, ID: p.Ref.ID, Title: p.Title, Column: p.ColumnName,
			IsDone: p.IsDone, Origin: p.Origin,
		}
		if p.Deadline != nil {
			cp.Deadline = p.Deadline.Format(dateLayout)
		}
		if p.WeekPlan != nil {
			cp.WeekPlan = *p.WeekPlan
		}
		canon.Planned = append(canon.Planned, cp)
	}
	return digestHash(canon)
}

// isoWeek returns the ISO-8601 week label of t ("2026-W46").
func isoWeek(t time.Time, loc *time.Location) string {
	year, week := t.In(loc).ISOWeek()
	return fmt.Sprintf("%d-W%02d", year, week)
}

// --- read-only adapters -----------------------------------------------------

func queryIN(query string, ids []string) (string, []any) {
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i] = "?"
		args[i] = id
	}
	return query + " IN (" + strings.Join(ph, ",") + ")", args
}

func groupRefs(refs []EntityRef) map[string][]string {
	out := map[string][]string{}
	for _, ref := range refs {
		out[ref.Type] = append(out[ref.Type], ref.ID)
	}
	return out
}

func (s *service) resolveBriefs(ctx context.Context, refs []EntityRef) (map[EntityRef]EntityBrief, error) {
	out := make(map[EntityRef]EntityBrief, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	h, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	db := h.DB()
	global := s.ws.Global().DB()
	groups := groupRefs(refs)

	for typ, ids := range groups {
		switch schema.EntityType(typ) {
		case schema.EntityNote:
			q, args := queryIN(`SELECT id, title, kind, deleted_at FROM notes WHERE id`, ids)
			if err := collectBriefs(ctx, db, typ, q, args, out, func(rows *sql.Rows) (string, string, bool, bool, error) {
				var id, title, kind string
				var deleted sql.NullString
				if err := rows.Scan(&id, &title, &kind, &deleted); err != nil {
					return "", "", false, false, err
				}
				return id, title, deleted.Valid, false, nil
			}); err != nil {
				return nil, err
			}
			for _, id := range ids {
				var kind string
				if err := db.QueryRowContext(ctx, `SELECT kind FROM notes WHERE id = ?`, id).Scan(&kind); err == nil {
					ref := EntityRef{Type: typ, ID: id}
					b := out[ref]
					b.NoteKind = kind
					out[ref] = b
				}
			}
		case schema.EntityDBRecord:
			q, args := queryIN(`SELECT id, title, deleted_at FROM db_records WHERE id`, ids)
			if err := collectBriefs(ctx, db, typ, q, args, out, func(rows *sql.Rows) (string, string, bool, bool, error) {
				var id, title string
				var deleted sql.NullString
				if err := rows.Scan(&id, &title, &deleted); err != nil {
					return "", "", false, false, err
				}
				return id, title, deleted.Valid, false, nil
			}); err != nil {
				return nil, err
			}
		case schema.EntityIdea:
			q, args := queryIN(`SELECT id, title, deleted_at FROM ideas WHERE id`, ids)
			if err := collectBriefs(ctx, db, typ, q, args, out, func(rows *sql.Rows) (string, string, bool, bool, error) {
				var id, title string
				var deleted sql.NullString
				if err := rows.Scan(&id, &title, &deleted); err != nil {
					return "", "", false, false, err
				}
				return id, title, deleted.Valid, false, nil
			}); err != nil {
				return nil, err
			}
		case schema.EntityEvidence:
			q, args := queryIN(`SELECT id, substr(content,1,80), deleted_at, ai_generated, confirmed FROM evidence WHERE id`, ids)
			if err := collectBriefs(ctx, db, typ, q, args, out, func(rows *sql.Rows) (string, string, bool, bool, error) {
				var id, content string
				var deleted sql.NullString
				var aiGenerated, confirmed int
				if err := rows.Scan(&id, &content, &deleted, &aiGenerated, &confirmed); err != nil {
					return "", "", false, false, err
				}
				return id, content, deleted.Valid, aiGenerated == 1 && confirmed == 0, nil
			}); err != nil {
				return nil, err
			}
		case schema.EntityExperiment:
			q, args := queryIN(`SELECT id, title, deleted_at FROM experiments WHERE id`, ids)
			if err := collectBriefs(ctx, db, typ, q, args, out, func(rows *sql.Rows) (string, string, bool, bool, error) {
				var id, title string
				var deleted sql.NullString
				if err := rows.Scan(&id, &title, &deleted); err != nil {
					return "", "", false, false, err
				}
				return id, title, deleted.Valid, false, nil
			}); err != nil {
				return nil, err
			}
		case schema.EntityRun:
			q, args := queryIN(`SELECT id, id, deleted_at FROM runs WHERE id`, ids)
			if err := collectBriefs(ctx, db, typ, q, args, out, func(rows *sql.Rows) (string, string, bool, bool, error) {
				var id, title string
				var deleted sql.NullString
				if err := rows.Scan(&id, &title, &deleted); err != nil {
					return "", "", false, false, err
				}
				return id, title, deleted.Valid, false, nil
			}); err != nil {
				return nil, err
			}
		case schema.EntitySnapshot:
			q, args := queryIN(`SELECT s.id, s.id, s.ai_output_id, o.status FROM snapshots s
				LEFT JOIN ai_outputs o ON o.id = s.ai_output_id WHERE s.id`, ids)
			if err := collectBriefs(ctx, db, typ, q, args, out, func(rows *sql.Rows) (string, string, bool, bool, error) {
				var id, title string
				var aiOutput, status sql.NullString
				if err := rows.Scan(&id, &title, &aiOutput, &status); err != nil {
					return "", "", false, false, err
				}
				unconfirmed := aiOutput.Valid && status.Valid && status.String == "draft"
				return id, title, false, unconfirmed, nil
			}); err != nil {
				return nil, err
			}
		case schema.EntityTask:
			q, args := queryIN(`SELECT id, title, deleted_at, archived_at FROM tasks WHERE id`, ids)
			if err := collectBriefs(ctx, db, typ, q, args, out, func(rows *sql.Rows) (string, string, bool, bool, error) {
				var id, title string
				var deleted, archived sql.NullString
				if err := rows.Scan(&id, &title, &deleted, &archived); err != nil {
					return "", "", false, false, err
				}
				return id, title, deleted.Valid, false, nil
			}); err != nil {
				return nil, err
			}
		case schema.EntityWeeklyReport:
			q, args := queryIN(`SELECT id, period_start || ' ~ ' || period_end, deleted_at FROM weekly_reports WHERE id`, ids)
			if err := collectBriefs(ctx, db, typ, q, args, out, func(rows *sql.Rows) (string, string, bool, bool, error) {
				var id, title string
				var deleted sql.NullString
				if err := rows.Scan(&id, &title, &deleted); err != nil {
					return "", "", false, false, err
				}
				return id, title, deleted.Valid, false, nil
			}); err != nil {
				return nil, err
			}
		case schema.EntityPaper:
			q, args := queryIN(`SELECT id, metadata, file_state FROM papers WHERE id`, ids)
			if err := collectBriefs(ctx, global, typ, q, args, out, func(rows *sql.Rows) (string, string, bool, bool, error) {
				var id, meta, state string
				if err := rows.Scan(&id, &meta, &state); err != nil {
					return "", "", false, false, err
				}
				title := jsonTitle(meta)
				return id, title, false, state == "missing", nil
			}); err != nil {
				return nil, err
			}
		case schema.EntityProject:
			q, args := queryIN(`SELECT id, name FROM projects WHERE id`, ids)
			if err := collectBriefs(ctx, global, typ, q, args, out, func(rows *sql.Rows) (string, string, bool, bool, error) {
				var id, name string
				if err := rows.Scan(&id, &name); err != nil {
					return "", "", false, false, err
				}
				return id, name, false, false, nil
			}); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func collectBriefs(ctx context.Context, db *sql.DB, typ, query string, args []any, out map[EntityRef]EntityBrief,
	scan func(*sql.Rows) (id, title string, broken, unconfirmed bool, err error)) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("weeklyreview: resolve briefs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		id, title, broken, unconfirmed, err := scan(rows)
		if err != nil {
			return fmt.Errorf("weeklyreview: resolve briefs: %w", err)
		}
		out[EntityRef{Type: typ, ID: id}] = EntityBrief{
			Title: title, Broken: broken, AIContentUnconfirmed: unconfirmed,
		}
	}
	return rows.Err()
}

func jsonTitle(metadata string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(metadata), &m); err != nil {
		return ""
	}
	for _, key := range []string{"title", "Title", "name"} {
		if v, ok := m[key].(string); ok && v != "" {
			return v
		}
	}
	var nested struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(metadata), &nested); err == nil {
		return nested.Title
	}
	return ""
}

func (s *service) blockedTasks(ctx context.Context) ([]BlockedTask, error) {
	h, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := h.DB().QueryContext(ctx,
		`SELECT t.id, t.title, t."column", COALESCE(c.is_done, 0)
		   FROM tasks t
		   LEFT JOIN task_columns c ON c.name = t."column"
		  WHERE t.blocked = 1 AND t.deleted_at IS NULL AND t.archived_at IS NULL
		  ORDER BY t.deadline IS NULL, t.deadline, t.id`)
	if err != nil {
		return nil, fmt.Errorf("weeklyreview: blocked tasks: %w", err)
	}
	defer rows.Close()
	out := []BlockedTask{}
	for rows.Next() {
		var (
			task BlockedTask
			done int
		)
		task.Ref.Type = string(schema.EntityTask)
		if err := rows.Scan(&task.Ref.ID, &task.Title, &task.ColumnName, &done); err != nil {
			return nil, fmt.Errorf("weeklyreview: blocked tasks: %w", err)
		}
		task.IsDone = done != 0
		out = append(out, task)
	}
	return out, rows.Err()
}

func (s *service) plannedTasks(ctx context.Context, period Period, nextISO, currentISO string, loc *time.Location) ([]PlannedTask, error) {
	h, err := s.handle(ctx)
	if err != nil {
		return nil, err
	}
	nextStart := period.EndExclusive
	nextEnd := period.EndExclusive.AddDate(0, 0, period.Days())
	rows, err := h.DB().QueryContext(ctx,
		`SELECT t.id, t.title, t."column", COALESCE(c.is_done, 0), t.deadline, t.week_plan
		   FROM tasks t
		   LEFT JOIN task_columns c ON c.name = t."column"
		  WHERE t.deleted_at IS NULL AND t.archived_at IS NULL
		    AND COALESCE(c.is_done, 0) = 0 AND t.completed_at IS NULL
		    AND (
		         t.week_plan = ?
		      OR (t.week_plan IS NULL AND t.deadline IS NOT NULL AND t.deadline >= ? AND t.deadline < ?)
		      OR t.week_plan = ?
		    )
		  ORDER BY t.id`,
		nextISO, nextStart.In(loc).Format(dateLayout), nextEnd.In(loc).Format(dateLayout), currentISO)
	if err != nil {
		return nil, fmt.Errorf("weeklyreview: planned tasks: %w", err)
	}
	defer rows.Close()
	out := []PlannedTask{}
	for rows.Next() {
		var (
			task     PlannedTask
			done     int
			deadline sql.NullString
			weekPlan sql.NullString
		)
		task.Ref.Type = string(schema.EntityTask)
		if err := rows.Scan(&task.Ref.ID, &task.Title, &task.ColumnName, &done, &deadline, &weekPlan); err != nil {
			return nil, fmt.Errorf("weeklyreview: planned tasks: %w", err)
		}
		task.IsDone = done != 0
		if deadline.Valid {
			if t, err := time.ParseInLocation(dateLayout, deadline.String, loc); err == nil {
				task.Deadline = &t
			}
		}
		if weekPlan.Valid {
			wp := weekPlan.String
			task.WeekPlan = &wp
		}
		switch {
		case weekPlan.Valid && weekPlan.String == nextISO:
			task.Origin = "week_plan"
		case !weekPlan.Valid && task.Deadline != nil && !task.Deadline.Before(nextStart) && task.Deadline.Before(nextEnd):
			task.Origin = "deadline"
		case weekPlan.Valid && weekPlan.String == currentISO:
			task.Origin = "carry_over"
		default:
			task.Origin = "manual"
		}
		out = append(out, task)
	}
	return out, rows.Err()
}
