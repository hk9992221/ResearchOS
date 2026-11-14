package pages

// The SPEC-08 task detail: the shell inspector's 详情 / 检查项 / 关联 tabs
// (SPEC-08 §6.1). It is opened from a board card, a table row and
// ports.RouteTask, keeps its unsaved buffers across frames, and turns a stale
// optimistic token into the documented reload-or-overwrite decision (§6.3
// rule 8).

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"github.com/egoist/mygo/ui/markdown"

	"researchos/internal/foundation/relations"
	"researchos/internal/foundation/schema"
	"researchos/internal/i18n"
	"researchos/internal/kanban"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// LinkCandidate is one entity the link picker offers. It carries only what the
// picker shows; the relation itself is always related_to from the UI
// (SPEC-08 §5.6).
type LinkCandidate struct {
	Type  schema.EntityType
	ID    string
	Title string
}

// The tabs of the detail.
const (
	kanbanTabDetail = iota
	kanbanTabChecklist
	kanbanTabLinks
)

// The two modes of the description editor.
const (
	kanbanDescSource = iota
	kanbanDescPreview
)

// taskDetail is the inspector state of one task. Service data lives under mu;
// the form buffers belong to the frame that renders them and are reset from a
// fresh load through the applied counter.
type taskDetail struct {
	mu          sync.Mutex
	target      string
	project     string
	visible     bool
	loading     bool
	loaded      bool
	loadedErr   string
	apply       int
	applied     int
	task        kanban.Task
	checklist   []kanban.ChecklistItem
	subtasks    []kanban.TaskSummary
	links       []kanban.EntityLink
	version     kanban.Version
	candidates  []LinkCandidate
	candLoaded  bool
	candLoading bool
	candErr     string

	// Write status shown in the pane.
	saving    bool
	saveErr   string
	archiving bool
	deleting  bool

	// Frame-thread form state.
	tab         int
	title       string
	ed          *editor.State
	mode        int
	priority    string
	deadline    *time.Time
	blocked     bool
	seenBlocked bool
	labels      []string
	labelInput  string
	column      string
	dirty       bool
	staleOpen   bool
	staleMsg    string
	confirmDel  bool
	prompt      bool
	checkInput  string
	editItem    string
	editText    string
	// checkFlags are the persistent check boxes of the checklist: a toggle
	// writes them between frames, so they cannot be frame locals.
	checkFlags map[string]*bool
	// checkPending guards a second toggle while the first one is in flight.
	checkPending map[string]bool
	pickerOpen   bool
	pickerGroup  string
	pickerQuery  string
	subtaskIn    string
	subtaskErr   string
	descErr      string
}

// open shows the detail of one task in the inspector.
func (d *taskDetail) open(a *app.App, k *Kanban, projectID, taskID string) {
	d.mu.Lock()
	same := d.visible && d.target == taskID && d.project == projectID
	d.mu.Unlock()
	if same && a.Inspector().Open {
		return
	}
	d.mu.Lock()
	d.target, d.project, d.visible = taskID, projectID, true
	d.loaded, d.loading = false, false
	d.loadedErr = ""
	d.candLoaded = false
	d.mu.Unlock()
	d.tab = kanbanTabDetail
	d.dirty = false
	d.staleOpen = false
	d.prompt = false
	d.confirmDel = false
	d.pickerOpen = false
	d.saveErr = ""
	d.show(a, k, projectID)
}

// show publishes the inspector body. The shell rebuilds it every frame, so it
// must read the state instead of capturing frame values.
func (d *taskDetail) show(a *app.App, k *Kanban, projectID string) {
	title := i18n.T("pages.kanban.detail_title")
	d.mu.Lock()
	if d.task.Title != "" {
		title = d.task.Title
	}
	d.mu.Unlock()
	st := d
	a.ShowInspector(title, func(v *components.UI) {
		st.body(v, a, k, projectID)
	})
}

// tick loads the task the first time it shows and handles the pane being
// closed: an unsaved form asks before its edits are thrown away (§6.3 rule 2).
func (d *taskDetail) tick(k *Kanban, a *app.App, projectID string) {
	d.mu.Lock()
	open := d.visible
	target := d.target
	loaded := d.loaded
	loading := d.loading
	d.mu.Unlock()
	if !open || target == "" {
		return
	}
	if !a.Inspector().Open {
		if d.hasUnsaved() {
			d.prompt = true
			d.show(a, k, projectID)
			return
		}
		d.reset()
		return
	}
	if !loaded && !loading {
		d.load(k, a, projectID)
	}
}

// load reads the full projection once, then resets the form.
func (d *taskDetail) load(k *Kanban, a *app.App, projectID string) {
	api := k.Services.Kanban
	if api == nil {
		return
	}
	d.mu.Lock()
	if d.loading || d.target == "" {
		d.mu.Unlock()
		return
	}
	target := d.target
	d.loading = true
	d.mu.Unlock()
	a.RunTask(i18n.T("pages.kanban.task_load"), func(ctx context.Context, t *app.Task) error {
		detail, err := api.GetTask(ctx, projectID, target)
		d.mu.Lock()
		d.loading = false
		if err != nil {
			d.loadedErr = kanbanErrorText(err)
		} else {
			d.task = detail.Task
			d.checklist = append([]kanban.ChecklistItem(nil), detail.Checklist...)
			d.subtasks = append([]kanban.TaskSummary(nil), detail.Subtasks...)
			d.links = append([]kanban.EntityLink(nil), detail.Links...)
			d.version = detail.Task.Version()
			d.loaded = true
			d.loadedErr = ""
			d.apply++
		}
		d.mu.Unlock()
		a.Notify()
		return nil
	})
}

// reload re-reads the task on demand, discarding the form. Used by the stale
// dialog's 重新加载 and by an overwrite retry.
func (d *taskDetail) reload(k *Kanban, a *app.App, projectID string) {
	d.mu.Lock()
	d.loaded = false
	d.loading = false
	d.mu.Unlock()
	d.load(k, a, projectID)
}

// close discards the detail and closes the pane.
func (d *taskDetail) close(a *app.App) {
	d.reset()
	a.HideInspector()
}

// reset drops the state the pane held.
func (d *taskDetail) reset() {
	d.mu.Lock()
	d.visible = false
	d.target = ""
	d.loaded = false
	d.loading = false
	d.loadedErr = ""
	d.task = kanban.Task{}
	d.checklist, d.subtasks, d.links = nil, nil, nil
	d.version = ""
	d.mu.Unlock()
	d.ed = nil
	d.dirty = false
	d.staleOpen = false
	d.staleMsg = ""
	d.prompt = false
	d.confirmDel = false
	d.pickerOpen = false
	d.saveErr = ""
	d.labelInput = ""
	d.subtaskIn = ""
	d.editItem = ""
}

// hasUnsaved reports a form or a description buffer that differs from the
// loaded task.
func (d *taskDetail) hasUnsaved() bool {
	if d.dirty {
		return true
	}
	return d.ed != nil && d.ed.Buffer.Modified()
}

// applyForm fills the buffers from a fresh load. It runs on the frame thread
// and only when the applied counter lags a load.
func (d *taskDetail) applyForm() {
	d.mu.Lock()
	if !d.loaded || d.applied == d.apply {
		d.mu.Unlock()
		return
	}
	d.applied = d.apply
	task := d.task
	d.mu.Unlock()

	d.title = task.Title
	d.priority = priorityLabel(task.Priority)
	if d.priority == "" {
		d.priority = i18n.T("pages.kanban.priority_none")
	}
	d.deadline = deadlineTime(task.Deadline)
	d.blocked = task.Blocked
	d.seenBlocked = task.Blocked
	d.checkFlags = map[string]*bool{}
	d.checkPending = map[string]bool{}
	d.labels = append([]string(nil), task.Labels...)
	d.column = task.Column
	buf := editor.NewBuffer(task.DescriptionMD)
	buf.SetLanguage(editor.LanguageFor("note.md"))
	d.ed = &editor.State{Buffer: buf}
	d.dirty = false
	d.saveErr = ""
	d.staleOpen = false
	d.mode = kanbanDescSource
	d.labelInput = ""
	d.descErr = ""
}

// body renders the inspector content.
func (d *taskDetail) body(v *components.UI, a *app.App, k *Kanban, projectID string) {
	d.applyForm()
	d.mu.Lock()
	loading, loadErr := d.loading, d.loadedErr
	loaded := d.loaded
	task := d.task
	archivedTask := task.ArchivedAt != nil
	saving, archiving, deleting := d.saving, d.archiving, d.deleting
	d.mu.Unlock()

	if !loaded {
		if loadErr != "" {
			v.Banner(components.BannerOptions{
				Tone:    components.Danger,
				Title:   i18n.T("pages.kanban.detail_load_failed"),
				Message: loadErr,
			})
			if v.Button(i18n.T("pages.kanban.btn_retry"), components.ButtonOptions{
				Variant: components.VariantSecondary,
			}).Clicked() {
				d.reload(k, a, projectID)
			}
			return
		}
		if loading {
			v.Skeleton(components.SkeletonOptions{Lines: 8})
		}
		return
	}
	if archivedTask {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.kanban.detail_archived_title"),
			Message: i18n.T("pages.kanban.detail_archived_message"),
		})
	}
	if d.saveErr != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.kanban.detail_save_failed"),
			Message: d.saveErr,
		})
	}

	v.Tabs(&d.tab,
		i18n.T("pages.kanban.detail_tab_detail"),
		i18n.T("pages.kanban.detail_tab_checklist"),
		i18n.T("pages.kanban.detail_tab_links"),
	).Label(i18n.T("pages.kanban.detail_tabs_label"))

	// The pane keeps its title and tabs in place; only the form under them
	// scrolls, so the last control of a long tab (标签, the subtask field)
	// stays reachable in a short window. The pane gives its height to the
	// column, so the scroll view takes what the header and tabs leave,
	// scrollbar and all.
	ui.Scroll(v.C).Grow(1).Children(func() {
		ui.Column(v.C).FillWidth().Gap(tokens.RelatedGap).Children(func() {
			switch d.tab {
			case kanbanTabChecklist:
				d.checklistTab(v, a, k, projectID)
			case kanbanTabLinks:
				d.linksTab(v, a, k, projectID)
			default:
				d.detailTab(v, a, k, projectID, archivedTask, saving, archiving, deleting)
			}
		})
	})
}

// detailTab is the form of the task itself.
func (d *taskDetail) detailTab(v *components.UI, a *app.App, k *Kanban, projectID string, archivedTask, saving, archiving, deleting bool) {
	readOnly := k.readOnly() || archivedTask
	field := v.Input(&d.title, components.FieldOptions{
		Label:    i18n.T("pages.kanban.field_title"),
		Disabled: readOnly,
	})
	if field.Changed() {
		d.dirty = true
	}
	if field.Submitted() && !readOnly {
		d.save(k, a, projectID, false)
	}

	d.description(v, readOnly)

	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).Wrap().Children(func() {
		if v.Select(&d.priority, components.SelectOptions{
			Label: i18n.T("pages.kanban.field_priority"),
			Options: []string{
				i18n.T("pages.kanban.priority_high"),
				i18n.T("pages.kanban.priority_medium"),
				i18n.T("pages.kanban.priority_low"),
				i18n.T("pages.kanban.priority_none"),
			},
			Disabled: readOnly,
		}).Changed() {
			d.dirty = true
		}
	})
	if deadlineField(v, &d.deadline, i18n.T("pages.kanban.field_deadline")) {
		d.dirty = true
	}
	v.Switch(&d.blocked, i18n.T("pages.kanban.field_blocked"), components.ToggleOptions{
		Disabled:    readOnly,
		Description: i18n.T("pages.kanban.field_blocked_desc"),
	})
	if d.blocked != d.seenBlocked {
		d.seenBlocked = d.blocked
		d.dirty = true
	}
	cols := k.columnNames()
	if d.column != "" {
		if v.Select(&d.column, components.SelectOptions{
			Label:    i18n.T("pages.kanban.field_column"),
			Options:  cols,
			Disabled: readOnly,
		}).Changed() {
			d.dirty = true
		}
	}
	d.labelsEditor(v, a, k, projectID, readOnly)
	d.subtasksList(v, a, k, projectID, readOnly)

	ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
		if !readOnly {
			if v.Button(i18n.T("pages.kanban.btn_save"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  saving,
				Disabled: saving,
			}).Clicked() {
				d.save(k, a, projectID, false)
			}
		}
		ui.Spacer(v.C)
		if archivedTask {
			if v.Button(i18n.T("pages.kanban.btn_unarchive"), components.ButtonOptions{
				Variant:  components.VariantSecondary,
				Loading:  archiving,
				Disabled: archiving || k.readOnly(),
			}).Clicked() {
				d.unarchive(k, a, projectID)
			}
		} else if !k.readOnly() {
			if v.Button(i18n.T("pages.kanban.btn_archive_task"), components.ButtonOptions{
				Variant:  components.VariantGhost,
				Loading:  archiving,
				Disabled: archiving,
			}).Clicked() {
				d.archive(k, a, projectID)
			}
		}
		if !archivedTask && !k.readOnly() {
			if v.Button(i18n.T("pages.kanban.btn_delete_task"), components.ButtonOptions{
				Variant:  components.VariantDanger,
				Loading:  deleting,
				Disabled: deleting,
			}).Clicked() {
				d.confirmDel = true
			}
		}
	})
}

// description is the Markdown source editor with its preview.
func (d *taskDetail) description(v *components.UI, readOnly bool) {
	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.Center).Children(func() {
		ui.Segmented(v.C, &d.mode,
			i18n.T("pages.kanban.mode_source"),
			i18n.T("pages.kanban.mode_preview"),
		).Label(i18n.T("pages.kanban.mode_label"))
	})
	if d.ed == nil {
		v.Skeleton(components.SkeletonOptions{Lines: 4})
		return
	}
	d.ed.ReadOnly = readOnly
	if d.mode == kanbanDescPreview {
		markdown.View(v.C, d.ed.Buffer.String(), &markdown.Options{}).FillWidth()
		return
	}
	editor.View(v.C, d.ed).FillWidth().Height(240)
	if d.ed.Buffer.Modified() {
		d.dirty = true
	}
}

// labelsEditor is the chips input: Enter adds a label, never during an IME
// composition (SPEC-08 §6.7).
func (d *taskDetail) labelsEditor(v *components.UI, a *app.App, k *Kanban, projectID string, readOnly bool) {
	ui.Column(v.C).FillWidth().Gap(tokens.Space1).Children(func() {
		ui.Text(v.C, i18n.T("pages.kanban.field_labels")).
			FontSize(tokens.Typography.Secondary.Size).
			TextColor(mygo.Color(v.T.Palette.TextSecondary))
		ui.Row(v.C).FillWidth().Gap(tokens.Space1).Wrap().Children(func() {
			for _, label := range d.labels {
				row := ui.Row(v.C).Gap(tokens.Space1).AlignItems(ui.Center)
				v.Badge(label, components.Neutral)
				if !readOnly {
					if v.IconButton(icons.Close, i18n.Tf("pages.kanban.label_remove", map[string]string{"label": label}), components.IconButtonOptions{
						Variant: components.VariantGhost,
						Size:    components.SizeSmall,
					}).Clicked() {
						d.removeLabel(k, a, projectID, label)
					}
				}
				_ = row
			}
		})
		if readOnly {
			return
		}
		field := v.Input(&d.labelInput, components.FieldOptions{
			Label:       i18n.T("pages.kanban.field_label_add"),
			Placeholder: i18n.T("pages.kanban.field_label_hint"),
		})
		if field.Submitted() {
			d.addLabel(k, a, projectID)
		}
	})
}

// subtasksList shows the live subtasks and creates one through ParentTaskID.
func (d *taskDetail) subtasksList(v *components.UI, a *app.App, k *Kanban, projectID string, readOnly bool) {
	d.mu.Lock()
	subs := append([]kanban.TaskSummary(nil), d.subtasks...)
	task := d.task
	d.mu.Unlock()
	ui.Column(v.C).FillWidth().Gap(tokens.Space1).Children(func() {
		ui.Text(v.C, i18n.T("pages.kanban.subtasks_title")).
			FontSize(tokens.Typography.Secondary.Size).
			FontWeight(int(tokens.WeightSemibold))
		if len(subs) == 0 {
			ui.Text(v.C, i18n.T("pages.kanban.subtasks_empty")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted))
		}
		for _, sub := range subs {
			row := ui.Row(v.C).FillWidth().Gap(tokens.Space2).AlignItems(ui.Center)
			row.Children(func() {
				title := ui.Text(v.C, kanbanTitle(sub.Title)).Grow(1).SingleLine()
				if title.RowClicked() {
					k.openTask(a, projectID, sub.ID)
				}
				v.Badge(sub.Column, components.Neutral)
				if sub.Priority != kanban.PriorityNone && sub.Priority != "" {
					v.Badge(priorityLabel(sub.Priority), priorityTone(sub.Priority))
				}
				if sub.Deadline != nil && *sub.Deadline != "" {
					text := ui.Text(v.C, *sub.Deadline).FontSize(tokens.Typography.Caption.Size)
					if kanbanOverdue(*sub.Deadline) {
						text.TextColor(mygo.Color(v.T.Palette.StatusDanger))
					}
				}
				if sub.Blocked {
					v.Icon(icons.Warning, icons.SizeDefault).TextColor(mygo.Color(v.T.Palette.StatusDanger))
				}
			})
		}
		if readOnly || task.ParentTaskID != nil {
			// Subtasks cannot nest: no add control on a subtask (AC-16).
			return
		}
		field := v.Input(&d.subtaskIn, components.FieldOptions{
			Label:       i18n.T("pages.kanban.field_subtask_add"),
			Placeholder: i18n.T("pages.kanban.field_subtask_hint"),
			Error:       d.subtaskErr,
		})
		if field.Submitted() {
			d.addSubtask(k, a, projectID, task)
		}
	})
}

// checklistTab is TASK-07: add, edit, toggle, remove and reorder.
func (d *taskDetail) checklistTab(v *components.UI, a *app.App, k *Kanban, projectID string) {
	readOnly := k.readOnly()
	d.mu.Lock()
	items := append([]kanban.ChecklistItem(nil), d.checklist...)
	d.mu.Unlock()

	if len(items) == 0 {
		ui.Text(v.C, i18n.T("pages.kanban.checklist_empty")).
			FontSize(tokens.Typography.Secondary.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted))
	}
	for i, item := range items {
		row := ui.Row(v.C).FillWidth().Gap(tokens.Space2).AlignItems(ui.Center)
		row.Children(func() {
			done := d.checkFlag(item.ID, item.Done)
			label := item.Text
			if d.editItem == item.ID {
				label = ""
			}
			v.Checkbox(done, label, components.ToggleOptions{Disabled: readOnly})
			if !readOnly && *done != item.Done && !d.checkInFlight(item.ID) {
				d.setChecklistDone(k, a, projectID, item.ID, *done)
			}
			if d.editItem == item.ID {
				field := v.Input(&d.editText, components.FieldOptions{
					Label:     i18n.T("pages.kanban.checklist_edit"),
					AutoFocus: true,
				})
				if field.Submitted() {
					d.updateChecklist(k, a, projectID, item.ID)
				}
			} else {
				text := ui.Text(v.C, item.Text).Grow(1).SingleLine()
				if text.RowClicked() && !readOnly {
					d.editItem, d.editText = item.ID, item.Text
				}
			}
			if readOnly {
				return
			}
			if v.IconButton(icons.ChevronUp, i18n.T("pages.kanban.checklist_up"), components.IconButtonOptions{
				Variant: components.VariantGhost, Size: components.SizeSmall, Disabled: i == 0,
			}).Clicked() && i > 0 {
				d.reorderChecklist(k, a, projectID, items, i, i-1)
			}
			if v.IconButton(icons.ChevronDown, i18n.T("pages.kanban.checklist_down"), components.IconButtonOptions{
				Variant: components.VariantGhost, Size: components.SizeSmall, Disabled: i+1 >= len(items),
			}).Clicked() && i+1 < len(items) {
				d.reorderChecklist(k, a, projectID, items, i, i+1)
			}
			if v.IconButton(icons.Trash, i18n.T("pages.kanban.checklist_remove"), components.IconButtonOptions{
				Variant: components.VariantGhost, Size: components.SizeSmall,
			}).Clicked() {
				d.removeChecklist(k, a, projectID, item.ID)
			}
		})
	}
	if readOnly {
		return
	}
	field := v.Input(&d.checkInput, components.FieldOptions{
		Label:       i18n.T("pages.kanban.field_checklist_add"),
		Placeholder: i18n.T("pages.kanban.field_checklist_hint"),
	})
	if field.Submitted() {
		d.addChecklist(k, a, projectID)
	}
}

// linksTab lists the task's relations and offers the grouped picker.
func (d *taskDetail) linksTab(v *components.UI, a *app.App, k *Kanban, projectID string) {
	d.mu.Lock()
	links := append([]kanban.EntityLink(nil), d.links...)
	d.mu.Unlock()
	if len(links) == 0 {
		ui.Text(v.C, i18n.T("pages.kanban.links_empty")).
			FontSize(tokens.Typography.Secondary.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted))
	}
	for _, link := range links {
		row := ui.Row(v.C).FillWidth().Gap(tokens.Space2).AlignItems(ui.Center)
		row.Children(func() {
			ui.Text(v.C, linkTypeLabel(link.DstType)).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextSecondary))
			ui.Text(v.C, link.DstTitle).Grow(1).SingleLine()
			if link.TargetState == relations.TargetSoftDeleted {
				// The row stays, labelled (AC-24).
				v.Badge(i18n.T("pages.kanban.link_target_deleted"), components.Warning)
			}
			if !k.readOnly() {
				if v.IconButton(icons.Close, i18n.T("pages.kanban.link_remove"), components.IconButtonOptions{
					Variant: components.VariantGhost,
					Size:    components.SizeSmall,
				}).Clicked() {
					d.removeLink(k, a, projectID, link)
				}
			}
		})
	}
	if k.readOnly() {
		return
	}
	if v.Button(i18n.T("pages.kanban.link_add"), components.ButtonOptions{
		Variant: components.VariantSecondary,
		Icon:    icons.Plus,
	}).Clicked() {
		d.pickerOpen = !d.pickerOpen
		if d.pickerOpen {
			d.loadCandidates(k, a, projectID)
		}
	}
	if !d.pickerOpen {
		return
	}
	d.picker(v, a, k, projectID)
}

// picker is the grouped relation picker: 笔记 / Idea / 实验 / Run.
func (d *taskDetail) picker(v *components.UI, a *app.App, k *Kanban, projectID string) {
	d.mu.Lock()
	cands := append([]LinkCandidate(nil), d.candidates...)
	loading, candErr := d.candLoading, d.candErr
	d.mu.Unlock()
	groups := []string{
		i18n.T("pages.kanban.link_group_note"),
		i18n.T("pages.kanban.link_group_idea"),
		i18n.T("pages.kanban.link_group_experiment"),
		i18n.T("pages.kanban.link_group_run"),
	}
	if d.pickerGroup == "" {
		d.pickerGroup = groups[0]
	}
	v.Select(&d.pickerGroup, components.SelectOptions{
		Label:   i18n.T("pages.kanban.link_type"),
		Options: groups,
	})
	v.Input(&d.pickerQuery, components.FieldOptions{
		Label:       i18n.T("pages.kanban.link_search"),
		Placeholder: i18n.T("pages.kanban.link_search_hint"),
	})
	if candErr != "" {
		ui.Text(v.C, candErr).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.StatusDanger))
	}
	if loading && len(cands) == 0 {
		v.Skeleton(components.SkeletonOptions{Lines: 3})
		return
	}
	want := linkGroupType(d.pickerGroup)
	query := strings.ToLower(strings.TrimSpace(d.pickerQuery))
	shown := 0
	for _, cand := range cands {
		if cand.Type != want {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(cand.Title), query) {
			continue
		}
		shown++
		if v.Button(cand.Title, components.ButtonOptions{
			Variant: components.VariantGhost,
			Size:    components.SizeSmall,
		}).Clicked() {
			d.addLink(k, a, projectID, cand)
			d.pickerOpen = false
			return
		}
	}
	if shown == 0 {
		ui.Text(v.C, i18n.T("pages.kanban.link_no_candidates")).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted))
	}
}

// dialogs renders the stale-token decision, the delete confirmation and the
// unsaved-changes prompt.
func (d *taskDetail) dialogs(v *components.UI, a *app.App, k *Kanban, projectID string) {
	if d.prompt {
		open := true
		v.Dialog(&open, components.DialogOptions{
			Title:       i18n.T("pages.kanban.unsaved_title"),
			Description: i18n.T("pages.kanban.unsaved_message"),
			Actions: func() {
				if v.Button(i18n.T("pages.kanban.unsaved_continue"), components.ButtonOptions{}).Clicked() {
					d.prompt = false
					open = true
				}
				if v.Button(i18n.T("pages.kanban.unsaved_discard"), components.ButtonOptions{
					Variant: components.VariantDanger,
				}).Clicked() {
					d.prompt = false
					d.close(a)
					open = false
				}
			},
		}, func() {})
		if !open {
			d.prompt = false
		}
	}
	if d.staleOpen {
		open := true
		v.Dialog(&open, components.DialogOptions{
			Title:       i18n.T("pages.kanban.stale_title"),
			Description: i18n.T("pages.kanban.stale_message"),
			Actions: func() {
				if v.Button(i18n.T("pages.kanban.stale_reload"), components.ButtonOptions{}).Clicked() {
					d.staleOpen = false
					d.reload(k, a, projectID)
					open = false
				}
				if v.Button(i18n.T("pages.kanban.stale_overwrite"), components.ButtonOptions{
					Variant: components.VariantPrimary,
				}).Clicked() {
					d.staleOpen = false
					d.save(k, a, projectID, true)
					open = false
				}
			},
		}, func() {})
		if !open {
			d.staleOpen = false
		}
	}
	if d.confirmDel {
		open := true
		d.mu.Lock()
		title := d.task.Title
		del := d.deleting
		d.mu.Unlock()
		v.Dialog(&open, components.DialogOptions{
			Title: i18n.T("pages.kanban.delete_title"),
			Actions: func() {
				if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
					d.confirmDel = false
					open = false
				}
				if v.Button(i18n.T("pages.kanban.delete_confirm"), components.ButtonOptions{
					Variant:  components.VariantDanger,
					Loading:  del,
					Disabled: del,
				}).Clicked() {
					d.delete(k, a, projectID)
				}
			},
		}, func() {
			ui.Text(v.C, i18n.Tf("pages.kanban.delete_message", map[string]string{"title": title})).
				FontSize(tokens.Typography.Body.Size)
		})
		if !open {
			d.confirmDel = false
		}
	}
}

// save writes the form. A stale token opens the reload-or-overwrite dialog
// instead of overwriting another window's edit (SPEC-08 §6.3 rule 8).
func (d *taskDetail) save(k *Kanban, a *app.App, projectID string, freshToken bool) {
	d.mu.Lock()
	if d.saving || d.target == "" {
		d.mu.Unlock()
		return
	}
	target := d.target
	version := d.version
	current := d.task
	d.mu.Unlock()

	title := strings.TrimSpace(d.title)
	if title == "" {
		d.descErr = i18n.T("pages.kanban.title_required")
		a.Toast(app.LevelWarning, i18n.T("pages.kanban.title_required"), "")
		return
	}
	desc := ""
	if d.ed != nil {
		desc = d.ed.Buffer.String()
	}
	priority := priorityFromLabel(d.priority)
	deadline := deadlineString(d.deadline)
	blocked := d.blocked
	labels := append([]string(nil), d.labels...)
	column := d.column

	in := kanban.UpdateInput{
		ID:            target,
		Title:         &title,
		DescriptionMD: &desc,
		Priority:      &priority,
		Deadline:      &deadline,
		Blocked:       &blocked,
		Labels:        &labels,
	}
	if column != "" && column != current.Column {
		col := column
		in.Column = &col
	}
	if !freshToken {
		v := version
		in.ExpectedUpdatedAt = &v
	}

	api := k.Services.Kanban
	d.mu.Lock()
	d.saving = true
	d.saveErr = ""
	d.mu.Unlock()

	a.RunTask(i18n.T("pages.kanban.task_save"), func(ctx context.Context, t *app.Task) error {
		expected := in
		if freshToken {
			// 覆盖: read the token another window left, then retry once.
			latest, gerr := api.GetTask(ctx, projectID, target)
			if gerr != nil {
				d.finishSave(kanbanErrorText(gerr))
				a.Notify()
				return nil
			}
			token := latest.Task.Version()
			expected.ExpectedUpdatedAt = &token
		}
		task, err := api.UpdateTask(ctx, projectID, expected)
		if err != nil {
			d.mu.Lock()
			d.saving = false
			d.saveErr = kanbanErrorText(err)
			if isKanbanError(err, kanban.ErrStaleRevision) {
				d.staleOpen = true
				d.staleMsg = i18n.T("pages.kanban.stale_message")
			}
			d.mu.Unlock()
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_save_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		if d.ed != nil {
			d.ed.Buffer.MarkSaved()
		}
		d.mu.Lock()
		d.saving = false
		d.task = task
		d.version = task.Version()
		d.mu.Unlock()
		d.dirty = false
		d.show(a, k, projectID)
		k.requestReload(a, projectID)
		a.Toast(app.LevelSuccess, i18n.T("pages.kanban.toast_saved"), "")
		return nil
	})
}

// finishSave only sets the error; kept small so the overwrite path stays one
// exit.
func (d *taskDetail) finishSave(msg string) {
	d.mu.Lock()
	d.saving = false
	d.saveErr = msg
	d.mu.Unlock()
}

// archive moves the task to the archive and closes the pane.
func (d *taskDetail) archive(k *Kanban, a *app.App, projectID string) {
	api := k.Services.Kanban
	d.mu.Lock()
	target := d.target
	if d.archiving || target == "" {
		d.mu.Unlock()
		return
	}
	d.archiving = true
	d.mu.Unlock()
	a.RunTask(i18n.T("pages.kanban.task_archive"), func(ctx context.Context, t *app.Task) error {
		_, err := api.ArchiveTask(ctx, projectID, target)
		d.mu.Lock()
		d.archiving = false
		d.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_archive_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		d.close(a)
		k.requestReload(a, projectID)
		a.Toast(app.LevelSuccess, i18n.T("pages.kanban.toast_archived"), "")
		return nil
	})
}

// unarchive returns the task to its column and position.
func (d *taskDetail) unarchive(k *Kanban, a *app.App, projectID string) {
	api := k.Services.Kanban
	d.mu.Lock()
	target := d.target
	d.archiving = true
	d.mu.Unlock()
	a.RunTask(i18n.T("pages.kanban.task_unarchive"), func(ctx context.Context, t *app.Task) error {
		_, err := api.UnarchiveTask(ctx, projectID, target)
		d.mu.Lock()
		d.archiving = false
		d.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_unarchive_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		d.reload(k, a, projectID)
		k.requestReload(a, projectID)
		a.Toast(app.LevelSuccess, i18n.T("pages.kanban.toast_unarchived"), "")
		return nil
	})
}

// delete soft-deletes the task into the recycle bin.
func (d *taskDetail) delete(k *Kanban, a *app.App, projectID string) {
	api := k.Services.Kanban
	d.mu.Lock()
	target := d.target
	if d.deleting || target == "" {
		d.mu.Unlock()
		return
	}
	d.deleting = true
	d.mu.Unlock()
	a.RunTask(i18n.T("pages.kanban.task_delete"), func(ctx context.Context, t *app.Task) error {
		err := api.DeleteTask(ctx, projectID, target)
		d.mu.Lock()
		d.deleting = false
		d.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_delete_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		d.confirmDel = false
		d.close(a)
		k.requestReload(a, projectID)
		a.Toast(app.LevelSuccess, i18n.T("pages.kanban.toast_deleted"), "")
		return nil
	})
}

// addLabel appends one label through LabelService, which returns the fresh
// token the next save must carry.
func (d *taskDetail) addLabel(k *Kanban, a *app.App, projectID string) {
	label := strings.TrimSpace(d.labelInput)
	if label == "" {
		return
	}
	for _, existing := range d.labels {
		if existing == label {
			d.labelInput = ""
			return
		}
	}
	api := k.Services.Kanban
	d.mu.Lock()
	target, version := d.target, d.version
	d.mu.Unlock()
	expected := version
	a.RunTask(i18n.T("pages.kanban.task_label"), func(ctx context.Context, t *app.Task) error {
		task, err := api.AddLabel(ctx, projectID, target, label, &expected)
		if err != nil {
			d.handleLabelError(a, k, projectID, err)
			return nil
		}
		d.mu.Lock()
		d.task = task
		d.version = task.Version()
		d.mu.Unlock()
		d.labels = append(d.labels, label)
		d.labelInput = ""
		k.requestReload(a, projectID)
		return nil
	})
}

// removeLabel drops one label.
func (d *taskDetail) removeLabel(k *Kanban, a *app.App, projectID, label string) {
	api := k.Services.Kanban
	d.mu.Lock()
	target, version := d.target, d.version
	d.mu.Unlock()
	expected := version
	a.RunTask(i18n.T("pages.kanban.task_label"), func(ctx context.Context, t *app.Task) error {
		task, err := api.RemoveLabel(ctx, projectID, target, label, &expected)
		if err != nil {
			d.handleLabelError(a, k, projectID, err)
			return nil
		}
		d.mu.Lock()
		d.task = task
		d.version = task.Version()
		d.mu.Unlock()
		kept := d.labels[:0]
		for _, l := range d.labels {
			if l != label {
				kept = append(kept, l)
			}
		}
		d.labels = kept
		k.requestReload(a, projectID)
		return nil
	})
}

// handleLabelError keeps a stale label edit from overwriting another window.
func (d *taskDetail) handleLabelError(a *app.App, k *Kanban, projectID string, err error) {
	if isKanbanError(err, kanban.ErrStaleRevision) {
		d.mu.Lock()
		d.staleOpen = true
		d.staleMsg = i18n.T("pages.kanban.stale_message")
		d.mu.Unlock()
		a.Notify()
		return
	}
	a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_label_fail"), kanbanErrorText(err))
	_ = k
	_ = projectID
	a.Notify()
}

// addSubtask creates a subtask through ParentTaskID.
func (d *taskDetail) addSubtask(k *Kanban, a *app.App, projectID string, parent kanban.Task) {
	title := strings.TrimSpace(d.subtaskIn)
	if title == "" {
		d.subtaskErr = i18n.T("pages.kanban.title_required")
		return
	}
	api := k.Services.Kanban
	parentID := parent.ID
	in := kanban.CreateInput{Title: title, Column: parent.Column, ParentTaskID: &parentID}
	d.subtaskErr = ""
	a.RunTask(i18n.T("pages.kanban.task_subtask"), func(ctx context.Context, t *app.Task) error {
		task, err := api.CreateTask(ctx, projectID, in)
		if err != nil {
			d.subtaskErr = kanbanErrorText(err)
			a.Notify()
			return nil
		}
		d.mu.Lock()
		d.subtasks = append(d.subtasks, kanban.TaskSummary{
			ID:           task.ID,
			Title:        task.Title,
			Column:       task.Column,
			ParentTaskID: task.ParentTaskID,
			Priority:     task.Priority,
			Deadline:     task.Deadline,
			Blocked:      task.Blocked,
			Labels:       task.Labels,
			UpdatedAt:    task.UpdatedAt,
		})
		d.mu.Unlock()
		d.subtaskIn = ""
		k.requestReload(a, projectID)
		return nil
	})
}

// addChecklist appends one checklist item.
func (d *taskDetail) addChecklist(k *Kanban, a *app.App, projectID string) {
	text := strings.TrimSpace(d.checkInput)
	if text == "" {
		return
	}
	api := k.Services.Kanban
	d.mu.Lock()
	target := d.target
	d.mu.Unlock()
	a.RunTask(i18n.T("pages.kanban.task_checklist"), func(ctx context.Context, t *app.Task) error {
		item, err := api.ChecklistAdd(ctx, projectID, target, text, nil)
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_checklist_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		d.mu.Lock()
		d.checklist = append(d.checklist, item)
		d.mu.Unlock()
		d.checkInput = ""
		k.requestReload(a, projectID)
		a.Notify()
		return nil
	})
}

// updateChecklist commits an edited item text.
func (d *taskDetail) updateChecklist(k *Kanban, a *app.App, projectID, itemID string) {
	text := strings.TrimSpace(d.editText)
	if text == "" {
		d.editItem = ""
		return
	}
	api := k.Services.Kanban
	a.RunTask(i18n.T("pages.kanban.task_checklist"), func(ctx context.Context, t *app.Task) error {
		item, err := api.ChecklistUpdate(ctx, projectID, itemID, text)
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_checklist_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		d.replaceChecklist(item)
		d.editItem = ""
		k.requestReload(a, projectID)
		a.Notify()
		return nil
	})
}

// setChecklistDone toggles an item; the backend never bumps the task version,
// so an open form keeps its token (TASK-07).
func (d *taskDetail) setChecklistDone(k *Kanban, a *app.App, projectID, itemID string, done bool) {
	api := k.Services.Kanban
	d.setCheckPending(itemID, true)
	a.RunTask(i18n.T("pages.kanban.task_checklist"), func(ctx context.Context, t *app.Task) error {
		item, err := api.ChecklistSetDone(ctx, projectID, itemID, done)
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_checklist_fail"), kanbanErrorText(err))
			d.clearCheckPending(itemID)
			a.Notify()
			return nil
		}
		d.replaceChecklist(item)
		k.requestReload(a, projectID)
		a.Notify()
		return nil
	})
}

// removeChecklist deletes one item.
func (d *taskDetail) removeChecklist(k *Kanban, a *app.App, projectID, itemID string) {
	api := k.Services.Kanban
	a.RunTask(i18n.T("pages.kanban.task_checklist"), func(ctx context.Context, t *app.Task) error {
		if err := api.ChecklistRemove(ctx, projectID, itemID); err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_checklist_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		d.mu.Lock()
		kept := d.checklist[:0]
		for _, it := range d.checklist {
			if it.ID != itemID {
				kept = append(kept, it)
			}
		}
		d.checklist = kept
		d.mu.Unlock()
		k.requestReload(a, projectID)
		a.Notify()
		return nil
	})
}

// reorderChecklist moves one item and persists the whole order.
func (d *taskDetail) reorderChecklist(k *Kanban, a *app.App, projectID string, items []kanban.ChecklistItem, from, to int) {
	if from == to || from < 0 || to < 0 || from >= len(items) || to >= len(items) {
		return
	}
	next := append([]kanban.ChecklistItem(nil), items...)
	moved := next[from]
	next = append(next[:from], next[from+1:]...)
	next = append(next, kanban.ChecklistItem{})
	copy(next[to+1:], next[to:])
	next[to] = moved
	ids := make([]string, 0, len(next))
	for _, it := range next {
		ids = append(ids, it.ID)
	}
	api := k.Services.Kanban
	d.mu.Lock()
	target := d.target
	d.checklist = next
	d.mu.Unlock()
	a.RunTask(i18n.T("pages.kanban.task_checklist"), func(ctx context.Context, t *app.Task) error {
		if err := api.ChecklistReorder(ctx, projectID, target, ids); err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_checklist_fail"), kanbanErrorText(err))
			d.reload(k, a, projectID)
			return nil
		}
		k.requestReload(a, projectID)
		a.Notify()
		return nil
	})
}

// replaceChecklist swaps one item in the cached list.
func (d *taskDetail) replaceChecklist(item kanban.ChecklistItem) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.checkPending, item.ID)
	if p := d.checkFlags[item.ID]; p != nil {
		*p = item.Done
	}
	for i := range d.checklist {
		if d.checklist[i].ID == item.ID {
			d.checklist[i] = item
			return
		}
	}
	d.checklist = append(d.checklist, item)
}

// checkFlag returns the persistent bool one checklist row binds to.
func (d *taskDetail) checkFlag(id string, done bool) *bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.checkFlags == nil {
		d.checkFlags = map[string]*bool{}
	}
	if p := d.checkFlags[id]; p != nil {
		return p
	}
	v := done
	p := &v
	d.checkFlags[id] = p
	return p
}

// checkInFlight reports a toggle already sent for one item.
func (d *taskDetail) checkInFlight(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.checkPending[id]
}

func (d *taskDetail) setCheckPending(id string, on bool) {
	d.mu.Lock()
	if d.checkPending == nil {
		d.checkPending = map[string]bool{}
	}
	d.checkPending[id] = on
	d.mu.Unlock()
}

func (d *taskDetail) clearCheckPending(id string) {
	d.mu.Lock()
	delete(d.checkPending, id)
	d.mu.Unlock()
}

// loadCandidates fills the picker's grouped list once.
func (d *taskDetail) loadCandidates(k *Kanban, a *app.App, projectID string) {
	d.mu.Lock()
	if d.candLoading || d.candLoaded {
		d.mu.Unlock()
		return
	}
	d.candLoading = true
	d.mu.Unlock()
	api := k.Services.Kanban
	a.RunTask(i18n.T("pages.kanban.task_load"), func(ctx context.Context, t *app.Task) error {
		cands, err := api.LinkCandidates(ctx, projectID)
		d.mu.Lock()
		d.candLoading = false
		if err != nil {
			d.candErr = kanbanErrorText(err)
		} else {
			d.candidates = cands
			d.candLoaded = true
			d.candErr = ""
		}
		d.mu.Unlock()
		a.Notify()
		return nil
	})
}

// addLink relates the task to one candidate with related_to.
func (d *taskDetail) addLink(k *Kanban, a *app.App, projectID string, cand LinkCandidate) {
	api := k.Services.Kanban
	d.mu.Lock()
	target := d.target
	d.mu.Unlock()
	a.RunTask(i18n.T("pages.kanban.task_link"), func(ctx context.Context, t *app.Task) error {
		if err := api.AddLink(ctx, projectID, target, cand.Type, cand.ID); err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_link_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		d.refreshLinks(k, a, projectID)
		return nil
	})
}

// removeLink drops one relation.
func (d *taskDetail) removeLink(k *Kanban, a *app.App, projectID string, link kanban.EntityLink) {
	api := k.Services.Kanban
	d.mu.Lock()
	target := d.target
	d.mu.Unlock()
	a.RunTask(i18n.T("pages.kanban.task_link"), func(ctx context.Context, t *app.Task) error {
		if err := api.RemoveLink(ctx, projectID, target, link.DstType, link.DstID); err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_link_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		d.refreshLinks(k, a, projectID)
		return nil
	})
}

// refreshLinks re-reads the relation list without touching the form.
func (d *taskDetail) refreshLinks(k *Kanban, a *app.App, projectID string) {
	api := k.Services.Kanban
	d.mu.Lock()
	target := d.target
	d.mu.Unlock()
	a.RunTask(i18n.T("pages.kanban.task_load"), func(ctx context.Context, t *app.Task) error {
		links, err := api.Links(ctx, projectID, target)
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.kanban.toast_link_fail"), kanbanErrorText(err))
			a.Notify()
			return nil
		}
		d.mu.Lock()
		d.links = links
		d.mu.Unlock()
		a.Notify()
		return nil
	})
}

// linkGroupType maps the picker's chosen group label to the entity type.
func linkGroupType(group string) schema.EntityType {
	switch group {
	case i18n.T("pages.kanban.link_group_note"):
		return schema.EntityNote
	case i18n.T("pages.kanban.link_group_idea"):
		return schema.EntityIdea
	case i18n.T("pages.kanban.link_group_experiment"):
		return schema.EntityExperiment
	}
	return schema.EntityRun
}

// linkTypeLabel names a relation target type.
func linkTypeLabel(t schema.EntityType) string {
	switch t {
	case schema.EntityNote:
		return i18n.T("pages.kanban.link_group_note")
	case schema.EntityIdea:
		return i18n.T("pages.kanban.link_group_idea")
	case schema.EntityExperiment:
		return i18n.T("pages.kanban.link_group_experiment")
	case schema.EntityRun:
		return i18n.T("pages.kanban.link_group_run")
	}
	return string(t)
}
