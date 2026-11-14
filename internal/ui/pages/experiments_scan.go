package pages

// The read-only file surfaces of SPEC-07: the scan preview of §6.3 and the
// Files panel of §6.8. Both render the same frozen ScanPlan the backend
// produced; the preview never rescans behind the user's back, and the Files
// panel's text preview comes from the deterministic context bundle, so what is
// shown is exactly what an analysis would send.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"researchos/internal/experiments"
	"researchos/internal/i18n"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// expKindOptions is the order of the kind filter: "all" then the kinds the
// backend can classify.
var expKindOptions = []experiments.FileKind{
	"",
	experiments.KindCode,
	experiments.KindReadme,
	experiments.KindConfig,
	experiments.KindData,
	experiments.KindMetrics,
	experiments.KindLog,
	experiments.KindImage,
	experiments.KindNotebook,
	experiments.KindBinary,
	experiments.KindOther,
}

// expKindLabelOrAll names one filter choice.
func expKindLabelOrAll(kind experiments.FileKind) string {
	if kind == "" {
		return i18n.T("pages.experiments.kind_all")
	}
	return expKindLabel(kind)
}

// expKindFromLabel maps a chosen filter label back to its kind.
func expKindFromLabel(label string) experiments.FileKind {
	for _, kind := range expKindOptions {
		if expKindLabelOrAll(kind) == label {
			return kind
		}
	}
	return ""
}

// ensurePlan starts the read-only Plan task once per target.
func (d *ExperimentDetail) ensurePlan(a *app.App, projectID string, target experiments.TargetRef) {
	d.mu.Lock()
	need := !d.busy["plan"] && d.plan == nil
	if need {
		d.busy["plan"] = true
	}
	d.mu.Unlock()
	if !need {
		return
	}
	api := d.API
	a.RunTask(i18n.T("pages.experiments.task_plan"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("pages.experiments.stage_scan"))
		plan, err := api.Plan(ctx, projectID, target, nil)
		d.mu.Lock()
		d.busy["plan"] = false
		if err != nil {
			d.scanErr = expErrText(err)
		} else {
			d.plan = plan
			d.scanErr = ""
			d.planAt = timeMark{set: true, at: expTime(nowUTC())}
		}
		d.mu.Unlock()
		a.Notify()
		return nil
	})
}

// ensureBundle builds the deterministic context bundle the Files preview and
// the context-preview dialog read. It performs no AI call.
func (d *ExperimentDetail) ensureBundle(a *app.App, projectID string, target experiments.TargetRef) {
	d.mu.Lock()
	need := !d.busy["bundle"] && d.bundle == nil
	if need {
		d.busy["bundle"] = true
	}
	d.mu.Unlock()
	if !need {
		return
	}
	api := d.API
	a.RunTask(i18n.T("pages.experiments.task_bundle"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("pages.experiments.stage_context"))
		bundle, err := api.PreviewContext(ctx, projectID, target, experiments.AnalysisOptions{})
		d.mu.Lock()
		d.busy["bundle"] = false
		if err != nil {
			d.bundleErr = expErrText(err)
		} else {
			d.bundle = bundle
			d.bundleErr = ""
		}
		d.mu.Unlock()
		a.Notify()
		return nil
	})
}

// scanPreview renders §6.3: summary, filters, rules entry and the tree.
func (d *ExperimentDetail) scanPreview(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, refs []experiments.DirReference, readOnly bool) {
	target := experiments.TargetRef{Type: experiments.TargetExperiment, ID: exp.Experiment.ID}
	if esp := exp.Experiment.DirRef; esp == nil || strings.TrimSpace(*esp) == "" {
		v.EmptyState(components.EmptyStateOptions{
			Icon:        icons.FolderOpen,
			Title:       i18n.T("pages.experiments.scan_nodir_title"),
			Reason:      i18n.T("pages.experiments.scan_nodir_reason"),
			ActionLabel: i18n.T("pages.experiments.btn_bind"),
			OnAction:    func() { d.openDirectoryDialog() },
		})
		return
	}
	d.ensurePlan(a, projectID, target)
	d.mu.Lock()
	plan := d.plan
	busyPlan := d.busy["plan"]
	busyScan := d.busy["scan"]
	d.mu.Unlock()

	ui.Column(v.C).FillWidth().Gap(tokens.SectionGap).Children(func() {
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
			if v.Button(i18n.T("pages.experiments.btn_back"), components.ButtonOptions{
				Variant: components.VariantGhost,
				Icon:    icons.Back,
			}).Clicked() {
				d.scanView = false
			}
			ui.Text(v.C, i18n.T("pages.experiments.scan_title")).
				FontSize(tokens.Typography.SectionTitle.Size).
				FontWeight(int(tokens.WeightSemibold))
			ui.Spacer(v.C)
			if v.Button(i18n.T("pages.experiments.btn_rules"), components.ButtonOptions{
				Variant:  components.VariantSecondary,
				Size:     components.SizeSmall,
				Disabled: readOnly,
				Tooltip:  expReadOnlyTooltip(readOnly),
			}).Clicked() {
				d.openRules(exp.Experiment.ScanRules)
			}
			if v.Button(i18n.T("pages.experiments.btn_run_scan"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Size:     components.SizeSmall,
				Icon:     icons.Scan,
				Loading:  busyScan,
				Disabled: readOnly || busyScan,
				Tooltip:  expReadOnlyTooltip(readOnly),
			}).Clicked() {
				d.runScan(a, projectID, target)
			}
		})
		ui.Text(v.C, i18n.T("pages.experiments.scan_readonly_note")).
			FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(v.T.Palette.TextSecondary))
		if plan == nil {
			if busyPlan {
				v.Skeleton(components.SkeletonOptions{Lines: 6})
			}
			return
		}
		d.planSummary(v, plan)
		d.treeFilters(v, plan)
		d.fileTree(v, plan)
	})
}

// planSummary is the §6.3 summary card.
func (d *ExperimentDetail) planSummary(v *components.UI, plan *experiments.ScanPlan) {
	v.Card(components.CardOptions{Title: i18n.T("pages.experiments.scan_summary")}, func() {
		ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).Wrap().AlignItems(ui.Start).Children(func() {
			d.stat(v, i18n.T("pages.experiments.scan_total_files"), itoa(plan.TotalFiles))
			d.stat(v, i18n.T("pages.experiments.scan_total_bytes"), humanBytes(plan.TotalBytes))
			d.stat(v, i18n.T("pages.experiments.scan_ai_files"), itoa(plan.IncludedFiles))
			d.stat(v, i18n.T("pages.experiments.scan_ai_bytes"), humanBytes(plan.IncludedBytes))
			d.stat(v, i18n.T("pages.experiments.scan_context_bytes"), humanBytes(plan.ContextBytes))
		})
		if plan.Truncated {
			v.Banner(components.BannerOptions{
				Tone:    components.Warning,
				Title:   i18n.T("pages.experiments.scan_truncated_title"),
				Message: i18n.T("pages.experiments.scan_truncated_message"),
			})
		}
		d.mu.Lock()
		info := d.scanInfo
		d.mu.Unlock()
		if info != "" {
			ui.Text(v.C, info).FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.TextSecondary))
		}
		for _, warning := range plan.Warnings {
			ui.Text(v.C, expWarningText(warning)).
				FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.StatusWarning))
		}
	})
}

// stat is one small number-and-label pair.
func (d *ExperimentDetail) stat(v *components.UI, label, value string) {
	ui.Column(v.C).Gap(tokens.Space1).MinWidth(tokens.Space12 * 4).Children(func() {
		ui.Text(v.C, value).FontSize(tokens.Typography.CardTitle.Size).
			FontWeight(int(tokens.WeightSemibold)).FontFeatures("tnum")
		ui.Text(v.C, label).FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted))
	})
}

// expWarningText states one scan warning.
func expWarningText(warning experiments.Warning) string {
	text := warning.Code
	if warning.Path != "" {
		text += " · " + warning.Path
	}
	if warning.Message != "" {
		text += " · " + warning.Message
	}
	return text
}

// treeFilters renders the two toggles of §6.3 plus the kind filter.
func (d *ExperimentDetail) treeFilters(v *components.UI, plan *experiments.ScanPlan) {
	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.Center).Wrap().Children(func() {
		seg := ui.Segmented(v.C, &d.planMode,
			i18n.T("pages.experiments.filter_all"),
			i18n.T("pages.experiments.filter_send"),
			i18n.T("pages.experiments.filter_excluded"),
		)
		seg.Label(i18n.T("pages.experiments.filter_label"))
		if seg.Changed() {
			d.selected = ""
		}
		current := d.planKind
		if current == "" {
			current = i18n.T("pages.experiments.kind_all")
		}
		options := make([]string, 0, len(expKindOptions))
		for _, kind := range expKindOptions {
			options = append(options, expKindLabelOrAll(kind))
		}
		sel := v.Select(&current, components.SelectOptions{
			Label:   i18n.T("pages.experiments.filter_kind"),
			Options: options,
			Compact: true,
		})
		if sel.Changed() {
			d.planKind = current
			if d.planKind == i18n.T("pages.experiments.kind_all") {
				d.planKind = ""
			}
			d.selected = ""
		}
		ui.Row(v.C).Grow(1).MinWidth(tokens.Space12 * 8).Children(func() {
			v.SearchInput(&d.planQuery, components.SearchOptions{
				Label:       i18n.T("pages.experiments.filter_path"),
				Placeholder: i18n.T("pages.experiments.filter_path_hint"),
			})
		})
		ui.Text(v.C, i18n.Tf("pages.experiments.tree_count", map[string]string{
			"files":    itoa(len(plan.Entries)),
			"included": itoa(plan.IncludedFiles),
		})).FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
	})
}

// fileTree renders the plan as a directory tree.
func (d *ExperimentDetail) fileTree(v *components.UI, plan *experiments.ScanPlan) {
	roots := d.buildTree(plan)
	if len(roots) == 0 {
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.FolderOpen,
			Title:  i18n.T("pages.experiments.tree_empty_title"),
			Reason: i18n.T("pages.experiments.tree_empty_reason"),
		})
		return
	}
	shown := plan.TotalFiles
	ui.Column(v.C).FillWidth().Gap(tokens.Space1).Children(func() {
		ui.Tree(v.C, func() {
			for _, node := range roots {
				d.treeNode(v, node)
			}
		})
		if shown > expTreeLimit {
			ui.Text(v.C, i18n.Tf("pages.experiments.tree_paged", map[string]string{"limit": itoa(expTreeLimit)})).
				FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
		}
	})
}

// expTreeNode is one row of the preview tree.
type expTreeNode struct {
	name     string
	path     string
	dir      bool
	entry    *experiments.FileEntry
	reason   *experiments.ExclusionReason
	children []*expTreeNode
}

// buildTree groups the visible entries by directory and adds the pruned
// directories as greyed rows. The entry cap of §6.10 is applied here.
func (d *ExperimentDetail) buildTree(plan *experiments.ScanPlan) []*expTreeNode {
	root := &expTreeNode{dir: true}
	byPath := map[string]*expTreeNode{"": root}
	var ensureDir func(string) *expTreeNode
	ensureDir = func(path string) *expTreeNode {
		if node, ok := byPath[path]; ok {
			return node
		}
		parent := ensureDir(expParentDir(path))
		node := &expTreeNode{name: expBaseName(path), path: path, dir: true}
		parent.children = append(parent.children, node)
		byPath[path] = node
		return node
	}
	count := 0
	for i := range plan.Entries {
		entry := plan.Entries[i]
		if !d.visibleEntry(entry) {
			continue
		}
		count++
		if count > expTreeLimit {
			break
		}
		parent := ensureDir(expParentDir(entry.Path))
		copied := entry
		parent.children = append(parent.children, &expTreeNode{
			name:  expBaseName(entry.Path),
			path:  entry.Path,
			entry: &copied,
		})
	}
	if d.planMode != 2 {
		for i := range plan.Pruned {
			pruned := plan.Pruned[i]
			if !d.visiblePruned(pruned) {
				continue
			}
			parent := ensureDir(expParentDir(strings.TrimSuffix(pruned.Path, "/")))
			reason := pruned.Reason
			parent.children = append(parent.children, &expTreeNode{
				name:   expBaseName(strings.TrimSuffix(pruned.Path, "/")) + "/",
				path:   pruned.Path,
				dir:    true,
				reason: &reason,
			})
		}
	}
	sortTree(root)
	return root.children
}

// visibleEntry applies the mode, kind and text filters.
func (d *ExperimentDetail) visibleEntry(entry experiments.FileEntry) bool {
	if d.planKind != "" && expKindLabel(entry.Kind) != d.planKind {
		return false
	}
	if query := strings.ToLower(strings.TrimSpace(d.planQuery)); query != "" &&
		!strings.Contains(strings.ToLower(entry.Path), query) {
		return false
	}
	sends := entry.ContentEligible && entry.ExclusionReason == nil
	switch d.planMode {
	case 1:
		return sends
	case 2:
		return !sends
	}
	return true
}

// visiblePruned applies the same text filter to a pruned directory row.
func (d *ExperimentDetail) visiblePruned(entry experiments.ExcludedEntry) bool {
	query := strings.ToLower(strings.TrimSpace(d.planQuery))
	return query == "" || strings.Contains(strings.ToLower(entry.Path), query)
}

// sortTree orders directories first, then names.
func sortTree(node *expTreeNode) {
	sortNodes(node.children)
	for _, child := range node.children {
		if child.dir && len(child.children) > 0 {
			sortTree(child)
		}
	}
}

// sortNodes is a small insertion sort: trees are shallow and per-directory
// lists are short.
func sortNodes(nodes []*expTreeNode) {
	for i := 1; i < len(nodes); i++ {
		for j := i; j > 0 && expNodeLess(nodes[j], nodes[j-1]); j-- {
			nodes[j], nodes[j-1] = nodes[j-1], nodes[j]
		}
	}
}

// expNodeLess orders a directory before a file, then by name.
func expNodeLess(a, b *expTreeNode) bool {
	if a.dir != b.dir {
		return a.dir
	}
	return strings.ToLower(a.name) < strings.ToLower(b.name)
}

// expBaseName returns the last path component.
func expBaseName(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	path = strings.TrimSuffix(path, "/")
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

// treeNode renders one tree row (recursively for directories).
func (d *ExperimentDetail) treeNode(v *components.UI, node *expTreeNode) {
	if node.dir {
		open := d.dirOpen[node.path]
		if open == nil {
			flag := true
			open = &flag
			d.dirOpen[node.path] = open
		}
		label := node.name
		if node.reason != nil {
			label += "  ·  " + i18n.Tf("pages.experiments.tree_pruned", map[string]string{
				"reason": expReasonLabel(*node.reason),
			})
		}
		ui.TreeItem(v.C, label, open, func() {
			for _, child := range node.children {
				d.treeNode(v, child)
			}
		})
		return
	}
	if node.entry == nil {
		return
	}
	entry := node.entry
	label := expEntryLabel(entry)
	item := ui.TreeItem(v.C, label, nil, nil)
	item = item.Selected(d.selected == entry.Path)
	if item.Clicked() {
		d.selectFile(entry.Path)
	}
}

// selectFile chooses one entry for the preview and resets the editor cache.
func (d *ExperimentDetail) selectFile(path string) {
	if d.selected == path {
		return
	}
	d.selected = path
	d.editor = nil
	d.editorFor = ""
	d.editorLine = 0
}

// expEntryLabel states kind, size and the AI decision of one file.
func expEntryLabel(entry *experiments.FileEntry) string {
	parts := []string{expBaseName(entry.Path), expKindLabel(entry.Kind), humanBytes(entry.Size)}
	if entry.ExclusionReason != nil {
		parts = append(parts, i18n.Tf("pages.experiments.tree_excluded", map[string]string{
			"reason": expReasonLabel(*entry.ExclusionReason),
		}))
	} else if entry.ContentEligible {
		parts = append(parts, i18n.T("pages.experiments.tree_sends"))
	} else {
		parts = append(parts, i18n.T("pages.experiments.tree_metadata_only"))
	}
	if entry.Sampled {
		parts = append(parts, i18n.T("pages.experiments.tree_sampled"))
	}
	return strings.Join(parts, "  ·  ")
}

// runScan performs the cancellable §6.3 scan. A cancelled scan writes nothing;
// the service owns that guarantee.
func (d *ExperimentDetail) runScan(a *app.App, projectID string, target experiments.TargetRef) {
	d.mu.Lock()
	if d.busy["scan"] {
		d.mu.Unlock()
		return
	}
	d.busy["scan"] = true
	d.scanErr = ""
	d.scanInfo = ""
	d.mu.Unlock()
	api := d.API
	a.RunTask(i18n.T("pages.experiments.task_scan"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("pages.experiments.stage_scan"))
		result, err := api.RunScan(ctx, projectID, target, nil)
		t.Stage(i18n.T("pages.experiments.stage_extract"))
		d.mu.Lock()
		d.busy["scan"] = false
		if err != nil {
			if errors.Is(err, context.Canceled) {
				d.scanInfo = i18n.T("pages.experiments.cancelled")
			} else {
				d.scanErr = expErrText(err)
			}
		} else {
			d.plan = &result.Plan
			d.planAt = timeMark{set: true, at: expTime(nowUTC())}
			d.scanInfo = i18n.Tf("pages.experiments.scan_done", map[string]string{
				"files":    itoa(result.Plan.TotalFiles),
				"included": itoa(result.Plan.IncludedFiles),
				"warnings": itoa(len(result.Warnings)),
			})
			if result.GitInfo != nil {
				d.git = result.GitInfo
			}
		}
		d.mu.Unlock()
		if err == nil {
			if health, verifyErr := api.VerifyDirectory(ctx, projectID, target.ID); verifyErr == nil {
				d.mu.Lock()
				d.dirRefs = health
				d.mu.Unlock()
			}
		}
		a.Notify()
		return nil
	})
}

// filesTab renders §6.8: filters, tree and the selected file.
func (d *ExperimentDetail) filesTab(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, run *experiments.RunDetail) {
	target := experiments.TargetRef{Type: experiments.TargetExperiment, ID: exp.Experiment.ID}
	if run != nil {
		target = experiments.TargetRef{Type: experiments.TargetRun, ID: run.Run.ID}
	}
	d.ensurePlan(a, projectID, target)
	d.mu.Lock()
	plan := d.plan
	busy := d.busy["plan"]
	d.mu.Unlock()
	if plan == nil {
		if busy {
			v.Skeleton(components.SkeletonOptions{Lines: 5})
		}
		return
	}
	d.treeFilters(v, plan)
	ui.Column(v.C).FillWidth().Gap(tokens.ComponentGap).Children(func() {
		v.SplitPanel(components.SplitPanelOptions{
			ShowSecond: true,
			First: func() {
				d.fileTree(v, plan)
			},
			Second: func() {
				d.filePreview(v, a, projectID, target, plan)
			},
		})
	})
}

// filePreview renders the selected file: a read-only editor for text that
// enters the context, and a metadata card otherwise.
func (d *ExperimentDetail) filePreview(v *components.UI, a *app.App, projectID string, target experiments.TargetRef, plan *experiments.ScanPlan) {
	d.mu.Lock()
	selected := d.selected
	bundle := d.bundle
	d.mu.Unlock()
	if selected == "" {
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.FileText,
			Title:  i18n.T("pages.experiments.file_none_title"),
			Reason: i18n.T("pages.experiments.file_none_reason"),
		})
		return
	}
	var entry *experiments.FileEntry
	for i := range plan.Entries {
		if plan.Entries[i].Path == selected {
			copied := plan.Entries[i]
			entry = &copied
			break
		}
	}
	if entry == nil {
		ui.Text(v.C, selected).Font(tokens.SystemFonts.Mono)
		return
	}
	ui.Column(v.C).FillWidth().Gap(tokens.ComponentGap).Children(func() {
		d.fileMeta(v, a, plan, *entry)
		if content, sampled, ok := expBundleContent(bundle, selected); ok && expTextKind(entry.Kind) {
			d.fileEditor(v, entry.Path, content, sampled)
			return
		}
		if expTextKind(entry.Kind) {
			d.ensureBundle(a, projectID, target)
			reason := i18n.T("pages.experiments.file_not_in_context")
			if entry.ExclusionReason != nil {
				reason = i18n.Tf("pages.experiments.file_excluded_reason", map[string]string{
					"reason": expReasonLabel(*entry.ExclusionReason),
				})
			}
			ui.Text(v.C, reason).
				FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(v.T.Palette.TextSecondary))
		} else {
			ui.Text(v.C, i18n.T("pages.experiments.file_binary_note")).
				FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
		}
	})
}

// fileMeta is the metadata line every preview shows.
func (d *ExperimentDetail) fileMeta(v *components.UI, a *app.App, plan *experiments.ScanPlan, entry experiments.FileEntry) {
	ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Wrap().Children(func() {
		v.Badge(expKindLabel(entry.Kind), components.Neutral)
		ui.Text(v.C, humanBytes(entry.Size)).FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted)).FontFeatures("tnum")
		if entry.MTime != "" {
			ui.Text(v.C, entry.MTime).FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted)).FontFeatures("tnum")
		}
		if entry.SHA256 != nil && *entry.SHA256 != "" {
			ui.Text(v.C, i18n.Tf("pages.experiments.file_hash", map[string]string{
				"hash": expShortHash(*entry.SHA256),
			})).Font(tokens.SystemFonts.Mono).FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted))
		}
		if entry.HashSkipped != nil && *entry.HashSkipped != "" {
			ui.Text(v.C, i18n.Tf("pages.experiments.file_hash_skipped", map[string]string{
				"reason": *entry.HashSkipped,
			})).FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
		}
		ui.Spacer(v.C)
		if v.Button(i18n.T("pages.experiments.btn_reveal"), components.ButtonOptions{
			Variant: components.VariantGhost,
			Size:    components.SizeSmall,
			Icon:    icons.FolderOpen,
			Tooltip: i18n.T("pages.experiments.btn_reveal_tooltip"),
		}).Clicked() {
			expReveal(a, expParentDir(expAbsPath(plan, entry.Path)))
		}
	})
}

// fileEditor shows the sampled content in a read-only editor with the source
// line positioned when the click came from an analysis source.
func (d *ExperimentDetail) fileEditor(v *components.UI, path, content string, sampled bool) {
	d.mu.Lock()
	line := d.editorLine
	d.mu.Unlock()
	if d.editor == nil || d.editorFor != path {
		buf := editor.NewBuffer(content)
		buf.SetLanguage(editor.LanguageFor(expBaseName(path)))
		st := &editor.State{Buffer: buf, ReadOnly: true, SoftWrap: true, LineNumbers: editor.LineNumbersOn, Label: path}
		d.editor = st
		d.editorFor = path
	}
	if line > 0 && d.editor != nil {
		d.editor.GoTo(editor.Pos{Line: line - 1})
		d.editor.ScrollTo(line - 1)
		d.mu.Lock()
		d.editorLine = 0
		d.mu.Unlock()
	}
	ui.Column(v.C).Fill().Gap(tokens.Space1).Children(func() {
		ui.Text(v.C, path).Font(tokens.SystemFonts.Mono).
			FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted)).SingleLine()
		if sampled {
			ui.Text(v.C, i18n.T("pages.experiments.file_sampled_note")).
				FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
		}
		editor.View(v.C, d.editor).FillWidth().MinHeight(tokens.Space12 * 12).Grow(1)
	})
}

// expTextKind reports whether a kind has a readable text preview.
func expTextKind(kind experiments.FileKind) bool {
	switch kind {
	case experiments.KindBinary, experiments.KindImage:
		return false
	}
	return true
}

// expBundleContent finds one path's deterministic content sample.
func expBundleContent(bundle *experiments.ContextBundle, path string) (string, bool, bool) {
	if bundle == nil {
		return "", false, false
	}
	for i := range bundle.Entries {
		entry := bundle.Entries[i]
		if entry.Path == path {
			return entry.Content, entry.Sampled, true
		}
	}
	return "", false, false
}

// expAbsPath resolves a plan entry to the absolute path shown in the system
// file manager.
func expAbsPath(plan *experiments.ScanPlan, path string) string {
	root := strings.TrimRight(strings.ReplaceAll(plan.Root, "\\", "/"), "/")
	rel := strings.TrimLeft(strings.ReplaceAll(path, "\\", "/"), "/")
	return root + "/" + rel
}

// nowUTC is the page's clock for the session-only scan timestamp.
func nowUTC() time.Time {
	return time.Now().UTC()
}
