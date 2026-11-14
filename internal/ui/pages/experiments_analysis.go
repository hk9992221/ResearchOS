package pages

// The analysis surface of SPEC-07: AI Outline (§6.4), AI Results (§6.5), the
// append-only History (§6.6) and the read-only Git panel (§6.7).
//
// The deterministic parts (metric table, cross-run deltas, Git) are rendered
// separately from the AI prose: the model can explain them but never change a
// number. Unverified claims and unconfirmed sections are always visible.

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"

	"researchos/internal/experiments"
	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// outlineTab renders the latest outline of the experiment.
func (d *ExperimentDetail) outlineTab(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, readOnly bool) {
	d.mu.Lock()
	snap, meta := d.outline, d.outlineMeta
	d.mu.Unlock()
	ui.Column(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
		ui.Text(v.C, i18n.T("pages.experiments.outline_desc")).
			FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(v.T.Palette.TextSecondary))
		if snap == nil {
			v.EmptyState(components.EmptyStateOptions{
				Icon:        icons.Experiments,
				Title:       i18n.T("pages.experiments.outline_empty_title"),
				Reason:      i18n.T("pages.experiments.outline_empty_reason"),
				ActionLabel: expGenerateLabel(false, readOnly),
				OnAction:    func() { d.generate(a, projectID, exp, nil, experiments.AnalysisOutline) },
			})
			d.generationFooter(v, a, projectID, exp, nil, experiments.AnalysisOutline, nil, meta, readOnly)
			return
		}
		d.snapshotHeader(v, snap, meta)
		if d.editing && d.editFor == snap.ID {
			d.editPanel(v, a, projectID, snap, meta, readOnly)
		} else {
			d.snapshotView(v, a, projectID, snap, meta, readOnly)
		}
		d.generationFooter(v, a, projectID, exp, nil, experiments.AnalysisOutline, snap, meta, readOnly)
	})
}

// resultsTab renders the deterministic metrics, the comparison table and the
// AI interpretation of one target.
func (d *ExperimentDetail) resultsTab(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, run *experiments.RunDetail, readOnly bool) {
	d.mu.Lock()
	snap, meta := d.results, d.resultsMeta
	d.mu.Unlock()
	if snap == nil {
		v.EmptyState(components.EmptyStateOptions{
			Icon:        icons.Experiments,
			Title:       i18n.T("pages.experiments.results_empty_title"),
			Reason:      i18n.T("pages.experiments.results_empty_reason"),
			ActionLabel: expGenerateLabel(false, readOnly),
			OnAction:    func() { d.generate(a, projectID, exp, run, experiments.AnalysisResults) },
		})
		d.generationFooter(v, a, projectID, exp, run, experiments.AnalysisResults, nil, meta, readOnly)
		return
	}
	ui.Column(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
		d.snapshotHeader(v, snap, meta)
		ui.Text(v.C, i18n.T("pages.experiments.results_split_note")).
			FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(v.T.Palette.TextSecondary))
		v.Card(components.CardOptions{
			Title:       i18n.T("pages.experiments.metrics_title"),
			Description: i18n.T("pages.experiments.metrics_desc"),
		}, func() {
			d.metricsTable(v, snap)
			d.metricChart(v, snap)
		})
		d.comparisonCard(v, a, projectID, exp)
		if d.editing && d.editFor == snap.ID {
			d.editPanel(v, a, projectID, snap, meta, readOnly)
		} else {
			d.snapshotView(v, a, projectID, snap, meta, readOnly)
		}
		d.generationFooter(v, a, projectID, exp, run, experiments.AnalysisResults, snap, meta, readOnly)
	})
}

// expGenerateLabel names the generate button.
func expGenerateLabel(has bool, readOnly bool) string {
	if readOnly {
		return i18n.T("pages.experiments.btn_generate")
	}
	if has {
		return i18n.T("pages.experiments.btn_regenerate")
	}
	return i18n.T("pages.experiments.btn_generate")
}

// snapshotHeader states what the frozen copy is and where its live output is.
func (d *ExperimentDetail) snapshotHeader(v *components.UI, snap *experiments.Snapshot, meta *experiments.SnapshotSummary) {
	ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Wrap().Children(func() {
		ui.Text(v.C, expTime(snap.CreatedAt)).FontSize(tokens.Typography.Secondary.Size).
			TextColor(mygo.Color(v.T.Palette.TextSecondary)).FontFeatures("tnum")
		if meta != nil {
			tone, label := expAIStatus(meta.AIStatus)
			v.Badge(label, tone)
			if meta.HumanEdited {
				v.Badge(i18n.T("pages.experiments.ai_human_edited"), components.Warning)
			}
			if meta.ProviderName != "" || meta.ModelID != "" {
				ui.Text(v.C, strings.TrimSpace(meta.ProviderName+" · "+meta.ModelID)).
					FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
			}
		}
		ui.Spacer(v.C)
		ui.Text(v.C, i18n.T("pages.experiments.snapshot_frozen")).
			FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
	})
	if snap.AIAnalysis != nil && len(snap.AIAnalysis.Warnings) > 0 {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.experiments.snapshot_warnings"),
			Message: strings.Join(snap.AIAnalysis.Warnings, " · "),
		})
	}
}

// snapshotView renders the frozen sections, sources and unverified claims.
func (d *ExperimentDetail) snapshotView(v *components.UI, a *app.App, projectID string, snap *experiments.Snapshot, meta *experiments.SnapshotSummary, readOnly bool) {
	analysis := snap.AIAnalysis
	if analysis == nil {
		ui.Text(v.C, i18n.T("pages.experiments.snapshot_no_ai")).
			TextColor(mygo.Color(v.T.Palette.TextMuted))
		return
	}
	sections := analysis.Sections
	if len(sections) == 0 {
		markdown.View(v.C, analysis.ContentMD, d.md).FillWidth()
		return
	}
	shown := map[string]bool{}
	for _, section := range sections {
		shown[string(section.Key)] = true
		d.section(v, a, projectID, snap, section)
	}
	for _, claim := range analysis.UnverifiedClaims {
		if shown[claim.Section] {
			continue
		}
		d.claim(v, claim)
	}
}

// section renders one AI section with its availability badge and sources.
func (d *ExperimentDetail) section(v *components.UI, a *app.App, projectID string, snap *experiments.Snapshot, section experiments.AISection) {
	ui.Column(v.C).FillWidth().Gap(tokens.Space1).Children(func() {
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
			ui.Text(v.C, section.Title).
				FontSize(tokens.Typography.SectionTitle.Size).
				FontWeight(int(tokens.WeightSemibold))
			if section.Unconfirmed {
				v.Badge(i18n.T("pages.experiments.section_unconfirmed"), components.Warning)
				if section.UnconfirmedReason != nil && *section.UnconfirmedReason != "" {
					v.Tooltip(ui.Text(v.C, "ⓘ").TextColor(mygo.Color(v.T.Palette.StatusWarning)),
						*section.UnconfirmedReason)
				}
			} else if len(section.Sources) > 0 {
				v.Badge(i18n.T("pages.experiments.section_sourced"), components.Success)
			} else {
				v.Badge(i18n.T("pages.experiments.section_unsourced"), components.Neutral)
			}
		})
		if section.Key == experiments.SectionMetrics && snap.AIAnalysis != nil {
			ui.Text(v.C, i18n.T("pages.experiments.metrics_readonly_note")).
				FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
		}
		markdown.View(v.C, section.TextMD, d.md).FillWidth()
		for _, source := range section.Sources {
			line := expSourceLine(snap, source)
			label := source.Path
			if line > 0 {
				label += ":" + itoa(line)
			}
			if len(source.MetricKeys) > 0 {
				label += "  ·  " + strings.Join(source.MetricKeys, ", ")
			}
			ui.Row(v.C).Gap(tokens.Space2).AlignItems(ui.Center).Children(func() {
				if v.Button(label, components.ButtonOptions{
					Variant: components.VariantGhost,
					Size:    components.SizeSmall,
					Icon:    icons.FileText,
				}).Clicked() {
					d.jumpToFile(source.Path, line)
				}
				if expSourceVerified(snap, source) {
					v.Badge(i18n.T("pages.experiments.source_verified"), components.Success)
				}
			})
		}
		for _, claim := range snap.AIAnalysis.UnverifiedClaims {
			if claim.Section == string(section.Key) {
				d.claim(v, claim)
			}
		}
	})
}

// claim renders one unverified claim inline; it is never hidden.
func (d *ExperimentDetail) claim(v *components.UI, claim experiments.UnverifiedClaim) {
	ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Start).Children(func() {
		badge := v.Badge(i18n.T("pages.experiments.claim_unverified"), components.Warning)
		if claim.Reason != "" {
			v.Tooltip(badge, claim.Reason)
		}
		ui.Text(v.C, claim.Text).
			FontSize(tokens.Typography.Secondary.Size).
			TextColor(mygo.Color(v.T.Palette.TextSecondary))
	})
}

// expSourceLine resolves the line of a source from its metric keys.
func expSourceLine(snap *experiments.Snapshot, source experiments.SourceRef) int {
	if len(source.MetricKeys) == 0 {
		return 0
	}
	for _, metric := range snap.Metrics.Metrics {
		for _, key := range source.MetricKeys {
			if metric.Key == key && metric.Source.Line != nil {
				return *metric.Source.Line
			}
		}
	}
	return 0
}

// expSourceVerified reports whether the source hash matches the frozen
// manifest (SPEC-07 §6.5).
func expSourceVerified(snap *experiments.Snapshot, source experiments.SourceRef) bool {
	if source.SHA256 == "" {
		return false
	}
	for _, entry := range snap.FileManifest {
		if entry.Path == source.Path && entry.SHA256 != nil && *entry.SHA256 == source.SHA256 {
			return true
		}
	}
	return false
}

// jumpToFile opens the Files tab on a source path, positioned at a line.
func (d *ExperimentDetail) jumpToFile(path string, line int) {
	d.tab = expTabFiles
	d.scanView = false
	d.selected = path
	d.editor = nil
	d.editorFor = ""
	d.editorLine = line
}

// editPanel edits the live ai_outputs text; the frozen snapshot copy never
// changes (SPEC-07 AC-13/AC-14).
func (d *ExperimentDetail) editPanel(v *components.UI, a *app.App, projectID string, snap *experiments.Snapshot, meta *experiments.SnapshotSummary, readOnly bool) {
	d.mu.Lock()
	busy := d.busy["save"]
	d.mu.Unlock()
	v.Card(components.CardOptions{
		Title:       i18n.T("pages.experiments.edit_card_title"),
		Description: expEditDescription(meta),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				d.editing = false
				d.editFor = ""
				d.editText = ""
			}
			if v.Button(i18n.T("common.save"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  busy,
				Disabled: busy || readOnly,
			}).Clicked() {
				d.saveContent(a, projectID, snap)
			}
		},
	}, func() {
		v.Textarea(&d.editText, components.FieldOptions{
			Label:       i18n.T("pages.experiments.edit_field_label"),
			Description: i18n.T("pages.experiments.edit_field_desc"),
		})
	})
}

// expEditDescription explains the SPEC-02 state machine of the current output.
func expEditDescription(meta *experiments.SnapshotSummary) string {
	if meta != nil && strings.EqualFold(strings.TrimSpace(meta.AIStatus), "confirmed") {
		return i18n.T("pages.experiments.edit_confirmed_hint")
	}
	return i18n.T("pages.experiments.edit_draft_hint")
}

// generationFooter is the §6.4 footer: generate/regenerate, edit, confirm,
// context preview and cancel.
func (d *ExperimentDetail) generationFooter(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, run *experiments.RunDetail, kind experiments.AnalysisKind, snap *experiments.Snapshot, meta *experiments.SnapshotSummary, readOnly bool) {
	d.mu.Lock()
	busy := d.busy["gen-"+string(kind)]
	confirming := d.confirmingSnap != ""
	d.mu.Unlock()
	confirmed := meta != nil && strings.EqualFold(strings.TrimSpace(meta.AIStatus), "confirmed")
	available := d.API.AIAvailable()
	tooltip := expReadOnlyTooltip(readOnly)
	if !available && !readOnly {
		tooltip = i18n.T("pages.experiments.err_ai_unavailable")
	}
	ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Wrap().Children(func() {
		if v.Button(expGenerateLabel(snap != nil, readOnly), components.ButtonOptions{
			Variant:  components.VariantPrimary,
			Icon:     icons.Refresh,
			Loading:  busy,
			Disabled: busy || readOnly || !available,
			Tooltip:  tooltip,
		}).Clicked() {
			d.generate(a, projectID, exp, run, kind)
		}
		if !available {
			// The SPEC-07 AC-15 hint: the entry stays visible, disabled, and
			// points at the only place a provider can be assigned.
			v.Button(i18n.T("pages.experiments.btn_settings"), components.ButtonOptions{
				Variant: components.VariantGhost,
				Size:    components.SizeSmall,
				Icon:    icons.Settings,
				OnClick: func() { a.Navigate(ports.Route{Kind: ports.RouteSettings}) },
			})
		}
		if v.Button(i18n.T("pages.experiments.btn_context_preview"), components.ButtonOptions{
			Variant: components.VariantSecondary,
			Size:    components.SizeSmall,
			Icon:    icons.Scan,
		}).Clicked() {
			d.contextPre = true
		}
		if snap != nil && !readOnly {
			if v.Button(i18n.T("pages.experiments.btn_edit_output"), components.ButtonOptions{
				Variant: components.VariantGhost,
				Size:    components.SizeSmall,
			}).Clicked() {
				d.editing = true
				d.editFor = snap.ID
				d.editText = expSnapshotText(snap)
			}
		}
		ui.Spacer(v.C)
		if snap != nil && !confirmed && !readOnly {
			if v.Button(i18n.T("pages.experiments.btn_confirm"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Size:     components.SizeSmall,
				Loading:  confirming,
				Disabled: confirming,
			}).Clicked() {
				d.confirmSnapshot(a, projectID, snap.ID)
			}
		} else if confirmed {
			v.Badge(i18n.T("pages.experiments.ai_confirmed"), components.Success)
		}
		if snap != nil {
			ui.Text(v.C, i18n.T("pages.experiments.append_never_overwrite")).
				FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
		}
	})
}

// expSnapshotText is the live output text a snapshot was generated from.
func expSnapshotText(snap *experiments.Snapshot) string {
	if snap == nil || snap.AIAnalysis == nil {
		return ""
	}
	return snap.AIAnalysis.ContentMD
}

// generate runs one generation with the §6.4 stages and a cancellable task.
func (d *ExperimentDetail) generate(a *app.App, projectID string, exp *experiments.ExperimentDetail, run *experiments.RunDetail, kind experiments.AnalysisKind) {
	key := "gen-" + string(kind)
	d.mu.Lock()
	if d.busy[key] {
		d.mu.Unlock()
		return
	}
	d.busy[key] = true
	d.genErr = ""
	d.retryOut = ""
	d.mu.Unlock()
	api := d.API
	target := d.targetRef(run)
	expID := exp.Experiment.ID
	name := i18n.T("pages.experiments.task_outline")
	if kind == experiments.AnalysisResults {
		name = i18n.T("pages.experiments.task_results")
	}
	a.RunTask(name, func(ctx context.Context, t *app.Task) error {
		// The deterministic plan is the cancellation point before the model
		// call; the service re-scans inside the generation call itself.
		t.Stage(i18n.T("pages.experiments.stage_scan"))
		plan, planErr := api.Plan(ctx, projectID, target, nil)
		if planErr != nil {
			d.mu.Lock()
			d.busy[key] = false
			d.genErr = expErrText(planErr)
			d.mu.Unlock()
			a.Notify()
			return nil
		}
		d.mu.Lock()
		d.plan = plan
		d.planAt = timeMark{set: true, at: expTime(nowUTC())}
		d.mu.Unlock()
		t.Stage(i18n.T("pages.experiments.stage_extract"))
		t.Stage(i18n.T("pages.experiments.stage_context"))
		t.Stage(i18n.T("pages.experiments.stage_model"))
		var snap *experiments.Snapshot
		var err error
		if kind == experiments.AnalysisOutline {
			snap, err = api.GenerateOutline(ctx, projectID, expID, experiments.AnalysisOptions{})
		} else {
			snap, err = api.GenerateResults(ctx, projectID, target, experiments.AnalysisOptions{})
		}
		t.Stage(i18n.T("pages.experiments.stage_validate"))
		d.mu.Lock()
		d.busy[key] = false
		if err != nil {
			if errors.Is(err, context.Canceled) {
				d.genErr = i18n.T("pages.experiments.cancelled")
			} else {
				d.genErr = expErrText(err)
				var persist *experiments.SnapshotPersistError
				if errors.As(err, &persist) {
					d.retryOut = persist.AIOutputID
				}
			}
		} else {
			meta := expMetaFromSnapshot(snap)
			if kind == experiments.AnalysisOutline {
				d.outline = snap
				d.outlineMeta = meta
			} else {
				d.results = snap
				d.resultsMeta = meta
				d.metrics = &snap.Metrics
			}
		}
		d.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.experiments.toast_ai_failed"), expErrText(err))
			a.Notify()
			return nil
		}
		d.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.experiments.toast_ai_done"), "")
		a.Notify()
		return nil
	})
}

// expMetaFromSnapshot builds the history-shaped summary of a fresh snapshot so
// the header shows its draft state before the reload lands.
func expMetaFromSnapshot(snap *experiments.Snapshot) *experiments.SnapshotSummary {
	if snap == nil {
		return nil
	}
	kind := experiments.AnalysisOutline
	if snap.AIAnalysis != nil {
		kind = snap.AIAnalysis.Kind
	}
	meta := &experiments.SnapshotSummary{
		ID:           snap.ID,
		Target:       snap.Target,
		Kind:         kind,
		CreatedAt:    snap.CreatedAt,
		ProviderName: snap.ModelInfo.ProviderName,
		ModelID:      snap.ModelInfo.ModelID,
		GitCommit:    snap.GitInfo.CommitShort,
		MetricCount:  len(snap.Metrics.Metrics),
		AIStatus:     "draft",
	}
	if snap.AIAnalysis != nil {
		meta.Warnings = len(snap.AIAnalysis.Warnings)
	}
	return meta
}

// targetRef is the analysis target of the current route.
func (d *ExperimentDetail) targetRef(run *experiments.RunDetail) experiments.TargetRef {
	d.mu.Lock()
	exp := d.exp
	d.mu.Unlock()
	if run != nil {
		return experiments.TargetRef{Type: experiments.TargetRun, ID: run.Run.ID}
	}
	if exp != nil {
		return experiments.TargetRef{Type: experiments.TargetExperiment, ID: exp.Experiment.ID}
	}
	return experiments.TargetRef{Type: experiments.TargetExperiment}
}

// confirmSnapshot confirms the live output as the user (SPEC-02 only lets a
// user confirm).
func (d *ExperimentDetail) confirmSnapshot(a *app.App, projectID, snapshotID string) {
	d.mu.Lock()
	if d.confirmingSnap != "" {
		d.mu.Unlock()
		return
	}
	d.confirmingSnap = snapshotID
	d.confirmErr = ""
	d.mu.Unlock()
	api := d.API
	a.RunTask(i18n.T("pages.experiments.task_confirm"), func(ctx context.Context, t *app.Task) error {
		err := api.Confirm(ctx, projectID, snapshotID, ports.ActorUser)
		d.mu.Lock()
		d.confirmingSnap = ""
		if err != nil {
			d.confirmErr = expErrText(err)
		}
		d.mu.Unlock()
		if err != nil {
			a.Notify()
			return nil
		}
		d.setMeta(snapshotID, "confirmed", nil)
		a.Toast(app.LevelSuccess, i18n.T("pages.experiments.toast_confirmed"), "")
		a.Notify()
		return nil
	})
}

// setMeta updates the shown summary under the page lock; a nil status or
// edited leaves that field alone.
func (d *ExperimentDetail) setMeta(snapshotID, status string, edited *bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, meta := range []*experiments.SnapshotSummary{d.outlineMeta, d.resultsMeta} {
		if meta == nil || meta.ID != snapshotID {
			continue
		}
		if status != "" {
			meta.AIStatus = status
		}
		if edited != nil {
			meta.HumanEdited = *edited
		}
	}
}

// saveContent writes the edited text through SPEC-02; the snapshot is frozen.
func (d *ExperimentDetail) saveContent(a *app.App, projectID string, snap *experiments.Snapshot) {
	text := d.editText
	d.mu.Lock()
	if d.busy["save"] {
		d.mu.Unlock()
		return
	}
	d.busy["save"] = true
	d.saveErr = ""
	d.mu.Unlock()
	api := d.API
	snapshotID := snap.ID
	a.RunTask(i18n.T("pages.experiments.task_save_output"), func(ctx context.Context, t *app.Task) error {
		err := api.UpdateContent(ctx, projectID, snapshotID, text, ports.ActorUser)
		d.mu.Lock()
		d.busy["save"] = false
		if err != nil {
			d.saveErr = expErrText(err)
		}
		d.mu.Unlock()
		if err != nil {
			a.Notify()
			return nil
		}
		d.setMeta(snapshotID, "", new(true))
		d.editing = false
		d.editFor = ""
		a.Toast(app.LevelSuccess, i18n.T("pages.experiments.toast_saved_output"), "")
		a.Notify()
		return nil
	})
}

// metricsTable is the deterministic metric table of §6.5.
func (d *ExperimentDetail) metricsTable(v *components.UI, snap *experiments.Snapshot) {
	if len(snap.Metrics.Metrics) == 0 {
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Diagnostics,
			Title:  i18n.T("pages.experiments.metrics_empty_title"),
			Reason: i18n.T("pages.experiments.metrics_empty_reason"),
		})
		return
	}
	rows := expSortMetrics(snap.Metrics.Metrics, d.metricTable.Sort)
	table := v.Table(components.TableOptions{
		Columns: []components.Column{
			{Title: i18n.T("pages.experiments.metric_key"), ID: "key", Sortable: true},
			{Title: i18n.T("pages.experiments.metric_value"), ID: "value", Width: 140, Align: ui.End, Sortable: true},
			{Title: i18n.T("pages.experiments.metric_unit"), Width: 96},
			{Title: i18n.T("pages.experiments.metric_source"), ID: "source", Sortable: true},
		},
		Rows:  len(rows),
		State: d.metricTable,
		Key:   func(i int) any { return rows[i].Key },
		Cell: func(row, col int) {
			metric := rows[row]
			switch col {
			case 0:
				label := metric.Label
				if label == "" {
					label = metric.Key
				}
				ui.Text(v.C, label).SingleLine()
			case 1:
				ui.Text(v.C, expMetricValue(metric)).FontFeatures("tnum").SingleLine()
			case 2:
				if metric.Unit != nil {
					ui.Text(v.C, *metric.Unit).TextColor(mygo.Color(v.T.Palette.TextMuted))
				}
			case 3:
				if ui.Text(v.C, expMetricSource(metric.Source)).SingleLine().RowClicked() {
					line := 0
					if metric.Source.Line != nil {
						line = *metric.Source.Line
					}
					d.jumpToFile(metric.Source.Path, line)
				}
			}
		},
	})
	table.Height(expTableHeight(len(rows)))
	for _, unparsed := range snap.Metrics.Unparsed {
		ui.Text(v.C, i18n.Tf("pages.experiments.metric_unparsed", map[string]string{
			"path": unparsed.Path, "reason": unparsed.Reason,
		})).FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.StatusWarning))
	}
}

// expSortMetrics sorts the deterministic table.
func expSortMetrics(list []experiments.Metric, order *ui.SortOrder) []experiments.Metric {
	out := append([]experiments.Metric(nil), list...)
	if order == nil || order.Column == "" {
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch order.Column {
		case "value":
			av, bv := 0.0, 0.0
			if a.Value != nil {
				av = *a.Value
			}
			if b.Value != nil {
				bv = *b.Value
			}
			if order.Descending {
				return av > bv
			}
			return av < bv
		case "source":
			if order.Descending {
				return a.Source.Path > b.Source.Path
			}
			return a.Source.Path < b.Source.Path
		}
		if order.Descending {
			return strings.ToLower(a.Key) > strings.ToLower(b.Key)
		}
		return strings.ToLower(a.Key) < strings.ToLower(b.Key)
	})
	return out
}

// metricChart draws one snapshot's numeric metrics as bars. Display only: it
// adds no inference and offers no multi-run comparison (SPEC-07 §6.5/AC-20).
func (d *ExperimentDetail) metricChart(v *components.UI, snap *experiments.Snapshot) {
	type bar struct {
		label string
		value float64
	}
	var bars []bar
	maxAbs := 0.0
	for _, metric := range snap.Metrics.Metrics {
		if metric.Value == nil || len(bars) == 8 {
			continue
		}
		value := *metric.Value
		abs := value
		if abs < 0 {
			abs = -abs
		}
		if abs > maxAbs {
			maxAbs = abs
		}
		label := metric.Label
		if label == "" {
			label = metric.Key
		}
		bars = append(bars, bar{label: label, value: value})
	}
	if len(bars) == 0 || maxAbs == 0 {
		return
	}
	ui.Text(v.C, i18n.T("pages.experiments.chart_caption")).
		FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
	ui.Row(v.C).Gap(tokens.SectionGap).AlignItems(ui.End).Height(tokens.Space12 * 10).Children(func() {
		for _, item := range bars {
			item := item
			height := float32(item.value/maxAbs) * tokens.Space12 * 7
			if height < 2 {
				height = 2
			}
			if height < 0 {
				height = 2
			}
			ui.Column(v.C).Gap(tokens.Space1).AlignItems(ui.Center).Children(func() {
				ui.Box(v.C).Width(tokens.Space6).Height(height).
					Background(mygo.Color(v.T.Palette.AccentDefault)).Radius(tokens.RadiusSM)
				ui.Text(v.C, expFloat(item.value)).FontSize(tokens.Typography.Caption.Size).
					FontFeatures("tnum")
				ui.Text(v.C, item.label).FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(v.T.Palette.TextMuted)).SingleLine()
			})
		}
	})
}

// comparisonCard is the deterministic cross-run delta table. It is evidence,
// never AI text, and it is not an experiment-comparison entry point.
func (d *ExperimentDetail) comparisonCard(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail) {
	d.ensureComparison(a, projectID, exp.Experiment.ID)
	d.mu.Lock()
	comparison := d.comparison
	busy := d.busy["compare"]
	d.mu.Unlock()
	v.Card(components.CardOptions{
		Title:       i18n.T("pages.experiments.compare_title"),
		Description: i18n.T("pages.experiments.compare_desc"),
	}, func() {
		if busy && comparison == nil {
			v.Skeleton(components.SkeletonOptions{Lines: 3})
			return
		}
		if comparison == nil || len(comparison.Runs) < 2 || len(comparison.Rows) == 0 {
			ui.Text(v.C, i18n.T("pages.experiments.compare_empty")).
				FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
			return
		}
		first := comparison.Runs[0]
		last := comparison.Runs[len(comparison.Runs)-1]
		rows := comparison.Rows
		table := v.Table(components.TableOptions{
			Columns: []components.Column{
				{Title: i18n.T("pages.experiments.metric_key"), ID: "key", Sortable: true},
				{Title: first.Label, Width: 140, Align: ui.End},
				{Title: last.Label, Width: 140, Align: ui.End},
				{Title: i18n.T("pages.experiments.compare_delta"), Width: 120, Align: ui.End},
				{Title: i18n.T("pages.experiments.compare_pct"), Width: 96, Align: ui.End},
			},
			Rows:  len(rows),
			State: d.compareTable,
			Key:   func(i int) any { return rows[i].Key + "|" + expContextKey(rows[i]) },
			Cell: func(row, col int) {
				item := rows[row]
				switch col {
				case 0:
					ui.Text(v.C, item.Key).SingleLine()
				case 1:
					if metric, ok := item.Values[first.RunID]; ok {
						ui.Text(v.C, expMetricValue(metric)).FontFeatures("tnum")
					}
				case 2:
					if metric, ok := item.Values[last.RunID]; ok {
						ui.Text(v.C, expMetricValue(metric)).FontFeatures("tnum")
					}
				case 3:
					if value, ok := item.Delta[last.RunID]; ok {
						ui.Text(v.C, expFloat(value)).FontFeatures("tnum")
					}
				case 4:
					if value, ok := item.Pct[last.RunID]; ok {
						ui.Text(v.C, expFloat(value)+"%").FontFeatures("tnum")
					}
				}
			},
		})
		table.Height(expTableHeight(len(rows)))
	})
}

// expContextKey distinguishes rows with the same key but another context.
func expContextKey(row experiments.MetricRow) string {
	if len(row.Context) == 0 {
		return ""
	}
	keys := make([]string, 0, len(row.Context))
	for key := range row.Context {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+expAnyString(row.Context[key]))
	}
	return strings.Join(parts, ",")
}

// expAnyString renders a context value deterministically.
func expAnyString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return expFloat(v)
	case int:
		return itoa(v)
	case nil:
		return ""
	}
	return ""
}

// ensureComparison loads the deterministic comparison once.
func (d *ExperimentDetail) ensureComparison(a *app.App, projectID, experimentID string) {
	d.mu.Lock()
	need := !d.busy["compare"] && d.comparison == nil
	if need {
		d.busy["compare"] = true
	}
	d.mu.Unlock()
	if !need {
		return
	}
	api := d.API
	a.RunTask(i18n.T("pages.experiments.task_compare"), func(ctx context.Context, t *app.Task) error {
		cmp, err := api.CompareRuns(ctx, projectID, experimentID)
		d.mu.Lock()
		d.busy["compare"] = false
		if err == nil {
			d.comparison = cmp
		}
		d.mu.Unlock()
		a.Notify()
		return nil
	})
}

// historyTab is the append-only history of §6.6. There is no delete or edit
// entry point, and no experiment-comparison entry point.
func (d *ExperimentDetail) historyTab(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, snaps []experiments.SnapshotSummary) {
	d.mu.Lock()
	detail := d.historySnap
	d.mu.Unlock()
	if detail != nil {
		ui.Column(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
			ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
				if v.Button(i18n.T("pages.experiments.btn_back"), components.ButtonOptions{
					Variant: components.VariantGhost,
					Icon:    icons.Back,
				}).Clicked() {
					d.mu.Lock()
					d.historySnap = nil
					d.mu.Unlock()
				}
				ui.Text(v.C, i18n.T("pages.experiments.history_detail_title")).
					FontSize(tokens.Typography.SectionTitle.Size).
					FontWeight(int(tokens.WeightSemibold))
				ui.Spacer(v.C)
				v.Badge(i18n.T("pages.experiments.history_readonly"), components.Neutral)
			})
			d.snapshotHeader(v, detail, nil)
			d.historyMeta(v, detail)
			d.snapshotView(v, a, projectID, detail, nil, true)
			d.metricsTable(v, detail)
		})
		return
	}
	if len(snaps) == 0 {
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Clock,
			Title:  i18n.T("pages.experiments.history_empty_title"),
			Reason: i18n.T("pages.experiments.history_empty_reason"),
		})
		return
	}
	rows := expSortSnapshots(snaps, d.historyTable.Sort)
	table := v.Table(components.TableOptions{
		Columns: []components.Column{
			{Title: i18n.T("pages.experiments.history_col_time"), ID: "time", Width: 140, Sortable: true},
			{Title: i18n.T("pages.experiments.history_col_kind"), Width: 96},
			{Title: i18n.T("pages.experiments.history_col_target"), Width: 96},
			{Title: i18n.T("pages.experiments.history_col_model")},
			{Title: i18n.T("pages.experiments.history_col_commit"), Width: 96},
			{Title: i18n.T("pages.experiments.history_col_metrics"), Width: 88, Align: ui.End},
			{Title: i18n.T("pages.experiments.history_col_status"), Width: 96},
			{Title: i18n.T("pages.experiments.history_col_warnings"), Width: 88, Align: ui.End},
		},
		Rows:  len(rows),
		State: d.historyTable,
		Key:   func(i int) any { return rows[i].ID },
		Cell: func(row, col int) {
			item := rows[row]
			switch col {
			case 0:
				if ui.Text(v.C, expTime(item.CreatedAt)).FontFeatures("tnum").RowClicked() {
					d.openHistory(a, projectID, item.ID)
				}
			case 1:
				ui.Text(v.C, expAnalysisLabel(item.Kind))
			case 2:
				ui.Text(v.C, expTargetLabel(item.Target))
			case 3:
				ui.Text(v.C, strings.TrimSpace(item.ProviderName+" · "+item.ModelID)).SingleLine()
			case 4:
				ui.Text(v.C, item.GitCommit).Font(tokens.SystemFonts.Mono).
					FontSize(tokens.Typography.Caption.Size)
			case 5:
				ui.Text(v.C, itoa(item.MetricCount)).FontFeatures("tnum")
			case 6:
				tone, label := expAIStatus(item.AIStatus)
				v.Badge(label, tone)
			case 7:
				if item.Warnings > 0 {
					v.Badge(itoa(item.Warnings), components.Warning)
				}
			}
		},
	})
	table.Height(expTableHeight(len(rows)))
	ui.Text(v.C, i18n.T("pages.experiments.history_append_note")).
		FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
}

// historyMeta states the frozen evidence of one snapshot.
func (d *ExperimentDetail) historyMeta(v *components.UI, snap *experiments.Snapshot) {
	v.Card(components.CardOptions{Title: i18n.T("pages.experiments.history_meta_title")}, func() {
		meta := []string{
			i18n.Tf("pages.experiments.history_meta_id", map[string]string{"id": snap.ID}),
			i18n.Tf("pages.experiments.history_meta_provider", map[string]string{
				"provider": snap.ModelInfo.ProviderName,
				"model":    snap.ModelInfo.ModelID,
			}),
			i18n.Tf("pages.experiments.history_meta_git", map[string]string{
				"commit": snap.GitInfo.CommitShort,
				"branch": snap.GitInfo.Branch,
			}),
			i18n.Tf("pages.experiments.history_meta_manifest", map[string]string{
				"count": itoa(len(snap.FileManifest)),
			}),
		}
		for _, line := range meta {
			ui.Text(v.C, line).FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.TextSecondary))
		}
	})
}

// openHistory loads one snapshot for the read-only detail.
func (d *ExperimentDetail) openHistory(a *app.App, projectID, snapshotID string) {
	api := d.API
	a.RunTask(i18n.T("pages.experiments.task_history"), func(ctx context.Context, t *app.Task) error {
		snap, err := api.GetSnapshot(ctx, projectID, snapshotID)
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.experiments.toast_history_failed"), expErrText(err))
			a.Notify()
			return nil
		}
		d.mu.Lock()
		d.historySnap = snap
		d.mu.Unlock()
		a.Notify()
		return nil
	})
}

// gitTab is the read-only Git panel of §6.7: information, the fixed warning,
// and a diff viewer for a range or the working tree. There is no write button.
func (d *ExperimentDetail) gitTab(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, readOnly bool) {
	d.mu.Lock()
	git := d.git
	diff := d.diff
	d.mu.Unlock()
	if git == nil || !git.Detected {
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Info,
			Title:  i18n.T("pages.experiments.git_none_title"),
			Reason: i18n.T("pages.experiments.git_none_reason"),
		})
		return
	}
	ui.Column(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
		v.Card(components.CardOptions{Title: i18n.T("pages.experiments.git_card_title")}, func() {
			ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Wrap().Children(func() {
				ui.Text(v.C, git.RepoRoot).Font(tokens.SystemFonts.Mono).
					FontSize(tokens.Typography.Secondary.Size)
			})
			ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.Center).Wrap().Children(func() {
				d.gitField(v, i18n.T("pages.experiments.git_branch"), expOrNone(git.Branch))
				d.gitField(v, i18n.T("pages.experiments.git_commit"), expOrNone(git.Commit))
				if v.Button(i18n.T("pages.experiments.btn_copy_commit"), components.ButtonOptions{
					Variant:  components.VariantGhost,
					Size:     components.SizeSmall,
					Icon:     icons.Copy,
					Disabled: git.Commit == "",
				}).Clicked() {
					v.C.WriteClipboard(git.Commit)
				}
				d.gitField(v, i18n.T("pages.experiments.git_time"), expOrNone(git.CommitTime))
				ui.Column(v.C).Gap(tokens.Space1).Children(func() {
					ui.Text(v.C, i18n.T("pages.experiments.git_dirty_label")).
						FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
					tone := components.Success
					label := i18n.T("pages.experiments.value_no")
					if git.Dirty {
						tone = components.Warning
						label = i18n.T("pages.experiments.value_yes")
					}
					v.Badge(label, tone)
				})
				d.gitField(v, i18n.T("pages.experiments.git_changed"), itoa(git.ChangedFiles))
				d.gitField(v, i18n.T("pages.experiments.git_untracked"), itoa(git.UntrackedFiles))
				d.gitField(v, i18n.T("pages.experiments.git_captured"), expOrNone(git.CapturedAt))
			})
		})
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.experiments.git_warning_title"),
			Message: i18n.T("pages.experiments.git_warning_message"),
		})
		v.Card(components.CardOptions{
			Title:       i18n.T("pages.experiments.git_diff_title"),
			Description: i18n.T("pages.experiments.git_diff_desc"),
			Actions: func() {
				if v.Button(i18n.T("pages.experiments.btn_show_diff"), components.ButtonOptions{
					Variant: components.VariantSecondary,
					Size:    components.SizeSmall,
				}).Clicked() {
					d.loadDiff(a, projectID, git.RepoRoot)
				}
			},
		}, func() {
			ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.End).Wrap().Children(func() {
				ui.Row(v.C).Grow(1).MinWidth(tokens.Space12 * 6).Children(func() {
					v.Input(&d.diffFrom, components.FieldOptions{
						Label:       i18n.T("pages.experiments.git_diff_from"),
						Placeholder: i18n.T("pages.experiments.git_diff_from_hint"),
					})
				})
				ui.Row(v.C).Grow(1).MinWidth(tokens.Space12 * 6).Children(func() {
					v.Input(&d.diffTo, components.FieldOptions{
						Label:       i18n.T("pages.experiments.git_diff_to"),
						Placeholder: i18n.T("pages.experiments.git_diff_to_hint"),
					})
				})
			})
			if diff == nil {
				ui.Text(v.C, i18n.T("pages.experiments.git_diff_hint")).
					FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
				return
			}
			if diff.Truncated {
				v.Banner(components.BannerOptions{
					Tone:    components.Warning,
					Title:   i18n.T("pages.experiments.git_diff_truncated"),
					Message: i18n.T("pages.experiments.git_diff_truncated_message"),
				})
			}
			if len(diff.Files) == 0 && strings.TrimSpace(diff.Patch) == "" {
				ui.Text(v.C, i18n.T("pages.experiments.git_diff_empty")).
					FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
				return
			}
			for _, file := range diff.Files {
				ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
					ui.Text(v.C, file.Path).Font(tokens.SystemFonts.Mono).
						FontSize(tokens.Typography.Secondary.Size).Grow(1).SingleLine()
					ui.Text(v.C, "+"+itoa(file.Added)+" −"+itoa(file.Deleted)).
						FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
					if file.Binary {
						v.Badge(i18n.T("pages.experiments.git_diff_binary"), components.Neutral)
					}
				})
			}
			d.diffViewer(v, diff)
		})
	})
}

// gitField is one label/value pair of the Git card.
func (d *ExperimentDetail) gitField(v *components.UI, label, value string) {
	ui.Column(v.C).Gap(tokens.Space1).Children(func() {
		ui.Text(v.C, label).FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted))
		ui.Text(v.C, value).FontSize(tokens.Typography.Secondary.Size).SingleLine()
	})
}

// loadDiff reads the read-only diff of the chosen range or the working tree.
func (d *ExperimentDetail) loadDiff(a *app.App, projectID, dir string) {
	d.mu.Lock()
	if d.busy["diff"] {
		d.mu.Unlock()
		return
	}
	d.busy["diff"] = true
	d.diffErr = ""
	d.mu.Unlock()
	api := d.API
	opts := experiments.DiffOptions{From: strings.TrimSpace(d.diffFrom), To: strings.TrimSpace(d.diffTo), MaxBytes: 1 << 20}
	key := opts.From + "|" + opts.To
	a.RunTask(i18n.T("pages.experiments.task_diff"), func(ctx context.Context, t *app.Task) error {
		diff, err := api.Diff(ctx, projectID, dir, opts)
		d.mu.Lock()
		d.busy["diff"] = false
		if err != nil {
			d.diffErr = expErrText(err)
		} else {
			d.diff = diff
			d.diffAt = key
			d.diffView = nil
			d.diffFor = ""
		}
		d.mu.Unlock()
		a.Notify()
		return nil
	})
}

// diffViewer shows a single-file patch in the diff viewer and falls back to a
// read-only patch text when the range touches several files.
func (d *ExperimentDetail) diffViewer(v *components.UI, diff *experiments.GitDiff) {
	if len(diff.Files) == 1 {
		if d.diffView == nil || d.diffFor != d.diffAt {
			patch, err := editor.ParsePatch(diff.Patch)
			if err == nil && len(patch) == 1 {
				st := &editor.DiffState{Layout: editor.DiffUnified, ContextLines: 3}
				st.SetPatch(patch[0])
				d.diffView = st
				d.diffFor = d.diffAt
			}
		}
		if d.diffView != nil {
			editor.DiffView(v.C, d.diffView).FillWidth().MinHeight(tokens.Space12 * 12)
			return
		}
	}
	ui.Text(v.C, i18n.T("pages.experiments.git_diff_multi")).
		FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
	buf := editor.NewBuffer(diff.Patch)
	buf.SetLanguage(editor.LanguageNamed("diff"))
	st := &editor.State{Buffer: buf, ReadOnly: true, SoftWrap: true, Label: "git diff"}
	editor.View(v.C, st).FillWidth().MinHeight(tokens.Space12 * 12)
}

// contextDialog is the §6.4 context-preview entry: files, bytes, exclusions.
func (d *ExperimentDetail) contextDialog(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, run *experiments.RunDetail) {
	if !d.contextPre {
		return
	}
	target := d.targetRef(run)
	d.ensureBundle(a, projectID, target)
	d.mu.Lock()
	bundle := d.bundle
	busy := d.busy["bundle"]
	d.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       i18n.T("pages.experiments.context_title"),
		Description: i18n.T("pages.experiments.context_desc"),
		Width:       tokens.Space12 * 18,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				d.contextPre = false
			}
		},
	}, func() {
		if busy && bundle == nil {
			v.Skeleton(components.SkeletonOptions{Lines: 4})
			return
		}
		if bundle == nil {
			return
		}
		ui.Text(v.C, i18n.Tf("pages.experiments.context_totals", map[string]string{
			"files": itoa(len(bundle.Entries)),
			"bytes": components.HumanBytes(bundle.Bytes),
		})).FontSize(tokens.Typography.Secondary.Size)
		if bundle.Truncated {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("pages.experiments.context_truncated"),
				Message: i18n.T("pages.experiments.context_truncated_message"),
			})
		}
		for _, entry := range bundle.Entries {
			ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
				ui.Text(v.C, entry.Path).Font(tokens.SystemFonts.Mono).
					FontSize(tokens.Typography.Caption.Size).Grow(1).SingleLine()
				ui.Text(v.C, components.HumanBytes(entry.Bytes)).FontSize(tokens.Typography.Caption.Size).
					TextColor(mygo.Color(v.T.Palette.TextMuted)).FontFeatures("tnum")
				if entry.Sampled {
					v.Badge(i18n.T("pages.experiments.context_sampled"), components.Neutral)
				}
			})
		}
		if len(bundle.Excluded) > 0 {
			ui.Text(v.C, i18n.T("pages.experiments.context_excluded")).
				FontSize(tokens.Typography.Secondary.Size).
				FontWeight(int(tokens.WeightSemibold))
			for _, entry := range bundle.Excluded {
				ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
					ui.Text(v.C, entry.Path).Font(tokens.SystemFonts.Mono).
						FontSize(tokens.Typography.Caption.Size).Grow(1).SingleLine()
					v.Badge(expReasonLabel(entry.Reason), components.Neutral)
				})
			}
		}
	})
	if !open {
		d.contextPre = false
	}
}

// expSortSnapshots orders the append-only history; newest first by default.
func expSortSnapshots(list []experiments.SnapshotSummary, order *ui.SortOrder) []experiments.SnapshotSummary {
	out := append([]experiments.SnapshotSummary(nil), list...)
	if order != nil && order.Column == "time" && !order.Descending {
		sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
		return out
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
