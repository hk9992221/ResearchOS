package pages

// The dialogs of the experiment view: new Run, run-from-subdirectory, Idea
// linking, experiment metadata, directory bind/relocate and the scan-rule
// editor. Each one keeps its input on failure so the user can retry, and none
// of them writes anything into the bound directory.

import (
	"context"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"researchos/internal/experiments"
	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// dialogs renders every dialog the page owns. A nil/closed dialog renders
// nothing.
func (d *ExperimentDetail) dialogs(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, readOnly bool) {
	d.createRunDialog(v, a, projectID, exp, readOnly)
	d.candidatesDialog(v, a, projectID, exp, readOnly)
	d.linkDialog(v, a, projectID, exp, readOnly)
	d.editDialog(v, a, projectID, exp, readOnly)
	d.directoryDialog(v, a, projectID, exp, readOnly)
	d.rulesDialog(v, a, projectID, exp, readOnly)
	d.confirmSnapshotDialog(v, a, projectID)
}

// createRunDialog collects the run parameters and an optional directory.
func (d *ExperimentDetail) createRunDialog(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, readOnly bool) {
	if !d.createRunOpen || readOnly {
		return
	}
	d.mu.Lock()
	creating := d.busy["create_run"]
	errText := d.runErr
	d.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       i18n.T("pages.experiments.run_dialog_title"),
		Description: i18n.T("pages.experiments.run_dialog_desc"),
		Width:       tokens.Space12 * 15,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				d.createRunOpen = false
			}
			if v.Button(i18n.T("pages.experiments.run_create"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  creating,
				Disabled: creating,
			}).Clicked() {
				d.createRun(a, projectID, exp.Experiment.ID)
			}
		},
	}, func() {
		ui.Text(v.C, i18n.T("pages.experiments.run_params_label")).
			FontSize(tokens.Typography.Secondary.Size).
			FontWeight(int(tokens.WeightSemibold))
		for i := range d.runParams {
			i := i
			ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.End).Children(func() {
				ui.Row(v.C).Grow(1).Children(func() {
					v.Input(&d.runParams[i].Key, components.FieldOptions{
						Label: i18n.T("pages.experiments.run_param_key"),
					})
				})
				ui.Row(v.C).Grow(1).Children(func() {
					v.Input(&d.runParams[i].Value, components.FieldOptions{
						Label: i18n.T("pages.experiments.run_param_value"),
					})
				})
				if v.IconButton(icons.Close, i18n.T("pages.experiments.run_param_remove"), components.IconButtonOptions{
					Variant: components.VariantGhost,
					Size:    components.SizeSmall,
				}).Clicked() {
					d.runParams = append(d.runParams[:i], d.runParams[i+1:]...)
					if len(d.runParams) == 0 {
						d.runParams = []expParamRow{{}}
					}
				}
			})
		}
		if v.Button(i18n.T("pages.experiments.run_param_add"), components.ButtonOptions{
			Variant: components.VariantGhost,
			Size:    components.SizeSmall,
			Icon:    icons.Plus,
		}).Clicked() {
			d.runParams = append(d.runParams, expParamRow{})
		}
		ui.Text(v.C, i18n.T("pages.experiments.run_params_hint")).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted))
		d.runDirField(v)
		if errText != "" {
			ui.Text(v.C, errText).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !creating {
		d.createRunOpen = false
	}
}

// runDirField is the directory selector of a run: inherit by default, or pick
// an override. A dropped folder becomes the override.
func (d *ExperimentDetail) runDirField(v *components.UI) {
	ui.Row(v.C).Gap(tokens.ComponentGap).AlignItems(ui.End).Children(func() {
		ui.Row(v.C).Grow(1).Children(func() {
			v.Input(&d.runDir, components.FieldOptions{
				Label:       i18n.T("pages.experiments.run_dir_label"),
				Placeholder: i18n.T("pages.experiments.run_dir_inherit"),
				ReadOnly:    true,
				Description: i18n.T("pages.experiments.run_dir_desc"),
			})
		})
		if v.Button(i18n.T("pages.experiments.btn_choose_folder"), components.ButtonOptions{
			Icon: icons.FolderOpen,
		}).Clicked() {
			if d.Services.ChooseFolder != nil {
				if dir, err := d.Services.ChooseFolder(); err == nil && strings.TrimSpace(dir) != "" {
					d.runDir = dir
				}
			}
		}
		if v.Button(i18n.T("pages.experiments.run_dir_clear"), components.ButtonOptions{
			Variant: components.VariantGhost,
		}).Clicked() {
			d.runDir = ""
		}
	})
	zone := ui.Column(v.C).FillWidth().Padding(tokens.Space3).Radius(tokens.RadiusLG).
		Border(1, mygo.Color(v.Palette().BorderDefault))
	if zone.FileDragOver() {
		zone.Border(2, mygo.Color(v.Palette().AccentDefault))
	}
	zone.Children(func() {
		ui.Text(v.C, i18n.T("pages.experiments.drop_dir")).
			FontSize(tokens.Typography.Secondary.Size).
			TextColor(mygo.Color(v.Palette().TextMuted))
	})
	if files := zone.DroppedFiles(); len(files) > 0 {
		if dir := expFolderFromDrop(files); dir != "" {
			d.runDir = dir
		}
	}
}

// createRun writes one run with the collected parameters.
func (d *ExperimentDetail) createRun(a *app.App, projectID, experimentID string) {
	params := map[string]experiments.ParamValue{}
	for _, row := range d.runParams {
		key := strings.TrimSpace(row.Key)
		if key == "" {
			continue
		}
		params[key] = experiments.ParamValue{Value: row.Value}
	}
	d.mu.Lock()
	if d.busy["create_run"] {
		d.mu.Unlock()
		return
	}
	d.busy["create_run"] = true
	d.runErr = ""
	d.mu.Unlock()

	api := d.API
	dir := strings.TrimSpace(d.runDir)
	a.RunTask(i18n.T("pages.experiments.task_create_run"), func(ctx context.Context, t *app.Task) error {
		created, err := api.CreateRun(ctx, projectID, experiments.CreateRunInput{
			ExperimentID: experimentID,
			DirPath:      dir,
			Params:       params,
			Actor:        ports.ActorUser,
		})
		d.mu.Lock()
		d.busy["create_run"] = false
		if err != nil {
			d.runErr = expErrText(err)
		}
		d.mu.Unlock()
		if err != nil {
			a.Notify()
			return nil
		}
		d.createRunOpen = false
		d.runParams = []expParamRow{{}}
		d.runDir = ""
		d.invalidate()
		a.Toast(app.LevelSuccess, i18n.T("pages.experiments.toast_run_created"), expParamSummary(created.Params))
		a.Navigate(ports.Route{Kind: ports.RouteRun, ProjectID: projectID, EntityID: created.ID})
		return nil
	})
}

// candidatesDialog offers the first-level subdirectories as new runs.
func (d *ExperimentDetail) candidatesDialog(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, readOnly bool) {
	if !d.candOpen || readOnly {
		return
	}
	d.mu.Lock()
	list := append([]experiments.DirCandidate(nil), d.candidates...)
	errText := d.candErr
	d.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       i18n.T("pages.experiments.cand_title"),
		Description: i18n.T("pages.experiments.cand_desc"),
		Width:       tokens.Space12 * 15,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				d.candOpen = false
			}
		},
	}, func() {
		if errText != "" {
			ui.Text(v.C, errText).TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
		if len(list) == 0 && errText == "" {
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.FolderOpen,
				Title:  i18n.T("pages.experiments.cand_empty_title"),
				Reason: i18n.T("pages.experiments.cand_empty_reason"),
			})
		}
		for _, cand := range list {
			ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
				col := ui.Column(v.C).Grow(1)
				col.Children(func() {
					ui.Text(v.C, cand.Path).Font(tokens.SystemFonts.Mono).
						FontSize(tokens.Typography.Secondary.Size).SingleLine()
					ui.Text(v.C, i18n.Tf("pages.experiments.cand_files", map[string]string{"count": itoa(cand.FileCount)})).
						FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
				})
				if v.Button(i18n.T("pages.experiments.cand_create"), components.ButtonOptions{
					Size: components.SizeSmall,
					Icon: icons.Plus,
				}).Clicked() {
					d.createRunFromDir(a, projectID, exp.Experiment.ID, cand.Path)
				}
			})
		}
	})
	if !open {
		d.candOpen = false
	}
}

// createRunFromDir creates a run from one candidate subdirectory.
func (d *ExperimentDetail) createRunFromDir(a *app.App, projectID, experimentID, dir string) {
	api := d.API
	a.RunTask(i18n.T("pages.experiments.task_create_run"), func(ctx context.Context, t *app.Task) error {
		created, err := api.CreateRun(ctx, projectID, experiments.CreateRunInput{
			ExperimentID: experimentID,
			DirPath:      dir,
			Actor:        ports.ActorUser,
		})
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.experiments.toast_run_failed"), expErrText(err))
			a.Notify()
			return nil
		}
		d.candOpen = false
		d.invalidate()
		a.Toast(app.LevelSuccess, i18n.T("pages.experiments.toast_run_created"), dir)
		a.Navigate(ports.Route{Kind: ports.RouteRun, ProjectID: projectID, EntityID: created.ID})
		return nil
	})
}

// linkDialog links an Idea by id. The SPEC-06 selector is not bound into this
// workspace yet, so the page falls back to the documented id/title search.
func (d *ExperimentDetail) linkDialog(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, readOnly bool) {
	if !d.linkOpen || readOnly {
		return
	}
	d.mu.Lock()
	busy := d.busy["link"]
	errText := d.linkErr
	d.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       i18n.T("pages.experiments.link_title"),
		Description: i18n.T("pages.experiments.link_desc"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				d.linkOpen = false
			}
			if v.Button(i18n.T("pages.experiments.link_confirm"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  busy,
				Disabled: busy,
			}).Clicked() {
				d.linkIdea(a, projectID, exp.Experiment.ID)
			}
		},
	}, func() {
		field := v.Input(&d.linkID, components.FieldOptions{
			Label:       i18n.T("pages.experiments.link_field"),
			Placeholder: i18n.T("pages.experiments.link_field_hint"),
			AutoFocus:   true,
		})
		if field.Submitted() {
			d.linkIdea(a, projectID, exp.Experiment.ID)
		}
		ui.Text(v.C, i18n.T("pages.experiments.link_fallback")).
			FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
		if errText != "" {
			ui.Text(v.C, errText).TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !busy {
		d.linkOpen = false
	}
}

// linkIdea writes the validates relation.
func (d *ExperimentDetail) linkIdea(a *app.App, projectID, experimentID string) {
	ideaID := strings.TrimSpace(d.linkID)
	if ideaID == "" {
		return
	}
	d.mu.Lock()
	if d.busy["link"] {
		d.mu.Unlock()
		return
	}
	d.busy["link"] = true
	d.linkErr = ""
	d.mu.Unlock()
	api := d.API
	a.RunTask(i18n.T("pages.experiments.task_link"), func(ctx context.Context, t *app.Task) error {
		err := api.LinkIdea(ctx, projectID, experimentID, ideaID, ports.ActorUser)
		d.mu.Lock()
		d.busy["link"] = false
		if err != nil {
			d.linkErr = expErrText(err)
		}
		d.mu.Unlock()
		if err != nil {
			a.Notify()
			return nil
		}
		d.linkOpen = false
		d.linkID = ""
		d.invalidate()
		a.Toast(app.LevelSuccess, i18n.T("pages.experiments.toast_linked"), ideaID)
		a.Notify()
		return nil
	})
}

// editDialog changes the title and the purpose.
func (d *ExperimentDetail) editDialog(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, readOnly bool) {
	if !d.editOpen || readOnly {
		return
	}
	d.mu.Lock()
	busy := d.busy["edit"]
	errText := d.editErr
	d.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title: i18n.T("pages.experiments.edit_title"),
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				d.editOpen = false
			}
			if v.Button(i18n.T("common.save"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  busy,
				Disabled: busy,
			}).Clicked() {
				d.saveEdit(a, projectID, exp.Experiment.ID)
			}
		},
	}, func() {
		v.Input(&d.editTitle, components.FieldOptions{
			Label:     i18n.T("pages.experiments.field_title"),
			AutoFocus: true,
		})
		v.Textarea(&d.editPurpose, components.FieldOptions{
			Label: i18n.T("pages.experiments.field_purpose"),
		})
		if errText != "" {
			ui.Text(v.C, errText).TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !busy {
		d.editOpen = false
	}
}

// saveEdit writes the metadata change.
func (d *ExperimentDetail) saveEdit(a *app.App, projectID, experimentID string) {
	title := strings.TrimSpace(d.editTitle)
	if title == "" {
		a.Toast(app.LevelWarning, i18n.T("pages.experiments.title_required"), "")
		return
	}
	d.mu.Lock()
	if d.busy["edit"] {
		d.mu.Unlock()
		return
	}
	d.busy["edit"] = true
	d.editErr = ""
	d.mu.Unlock()
	api := d.API
	purpose := d.editPurpose
	a.RunTask(i18n.T("pages.experiments.task_edit"), func(ctx context.Context, t *app.Task) error {
		_, err := api.UpdateExperiment(ctx, projectID, experiments.UpdateExperimentInput{
			ID: experimentID, Title: &title, Purpose: &purpose,
		})
		d.mu.Lock()
		d.busy["edit"] = false
		if err != nil {
			d.editErr = expErrText(err)
		}
		d.mu.Unlock()
		if err != nil {
			a.Notify()
			return nil
		}
		d.editOpen = false
		d.invalidate()
		a.Notify()
		return nil
	})
}

// directoryDialog binds a directory for the first time or relocates a broken
// one. Relocation keeps the old snapshots' source paths (SPEC-07 AC-03).
func (d *ExperimentDetail) directoryDialog(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, readOnly bool) {
	if !d.dirOpenDlg || readOnly {
		return
	}
	d.mu.Lock()
	busy := d.busy["dir"]
	errText := d.dirErr
	d.mu.Unlock()
	relocating := exp.Experiment.DirRef != nil && strings.TrimSpace(*exp.Experiment.DirRef) != ""
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       expDirDialogTitle(relocating),
		Description: i18n.T("pages.experiments.dir_dialog_desc"),
		Width:       tokens.Space12 * 14,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				d.dirOpenDlg = false
			}
			if v.Button(i18n.T("pages.experiments.dir_save"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  busy,
				Disabled: busy,
			}).Clicked() {
				d.saveDirectory(a, projectID, exp.Experiment.ID, relocating, exp.Experiment.ScanRules)
			}
		},
	}, func() {
		ui.Row(v.C).Gap(tokens.ComponentGap).AlignItems(ui.End).Children(func() {
			ui.Row(v.C).Grow(1).Children(func() {
				v.Input(&d.dirPick, components.FieldOptions{
					Label:       i18n.T("pages.experiments.field_dir"),
					Placeholder: i18n.T("pages.experiments.field_dir_hint"),
					ReadOnly:    true,
				})
			})
			if v.Button(i18n.T("pages.experiments.btn_choose_folder"), components.ButtonOptions{
				Icon: icons.FolderOpen,
			}).Clicked() {
				if d.Services.ChooseFolder != nil {
					if dir, err := d.Services.ChooseFolder(); err == nil && strings.TrimSpace(dir) != "" {
						d.dirPick = dir
					}
				}
			}
		})
		zone := ui.Column(v.C).FillWidth().Padding(tokens.Space3).Radius(tokens.RadiusLG).
			Border(1, mygo.Color(v.Palette().BorderDefault))
		if zone.FileDragOver() {
			zone.Border(2, mygo.Color(v.Palette().AccentDefault))
		}
		zone.Children(func() {
			ui.Text(v.C, i18n.T("pages.experiments.drop_dir")).
				FontSize(tokens.Typography.Secondary.Size).TextColor(mygo.Color(v.Palette().TextMuted))
		})
		if files := zone.DroppedFiles(); len(files) > 0 {
			if dir := expFolderFromDrop(files); dir != "" {
				d.dirPick = dir
			}
		}
		if errText != "" {
			ui.Text(v.C, errText).TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !busy {
		d.dirOpenDlg = false
	}
}

// expDirDialogTitle distinguishes the first bind from a relocation.
func expDirDialogTitle(relocating bool) string {
	if relocating {
		return i18n.T("pages.experiments.relocate_title")
	}
	return i18n.T("pages.experiments.bind_title")
}

// saveDirectory binds or relocates the directory.
func (d *ExperimentDetail) saveDirectory(a *app.App, projectID, experimentID string, relocating bool, rules experiments.ScanRules) {
	path := strings.TrimSpace(d.dirPick)
	if path == "" {
		d.mu.Lock()
		d.dirErr = i18n.T("pages.experiments.dir_required")
		d.mu.Unlock()
		return
	}
	d.mu.Lock()
	if d.busy["dir"] {
		d.mu.Unlock()
		return
	}
	d.busy["dir"] = true
	d.dirErr = ""
	d.mu.Unlock()
	api := d.API
	a.RunTask(i18n.T("pages.experiments.task_bind"), func(ctx context.Context, t *app.Task) error {
		var err error
		if relocating {
			_, err = api.RelocateDirectory(ctx, projectID, experimentID, path, ports.ActorUser)
		} else {
			_, err = api.BindDirectory(ctx, projectID, experimentID, path, rules)
		}
		d.mu.Lock()
		d.busy["dir"] = false
		if err != nil {
			d.dirErr = expErrText(err)
		}
		d.mu.Unlock()
		if err != nil {
			a.Notify()
			return nil
		}
		d.dirOpenDlg = false
		d.dirPick = ""
		d.invalidate()
		a.Toast(app.LevelSuccess, i18n.T("pages.experiments.toast_dir_saved"), path)
		a.Notify()
		return nil
	})
}

// rulesDialog edits include/exclude lists and the size thresholds. `.git` is
// shown as force-excluded and cannot be re-enabled.
func (d *ExperimentDetail) rulesDialog(v *components.UI, a *app.App, projectID string, exp *experiments.ExperimentDetail, readOnly bool) {
	if !d.rulesOpen || readOnly {
		return
	}
	d.mu.Lock()
	busy := d.busy["rules"]
	errText := d.rulesErr
	d.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       i18n.T("pages.experiments.rules_title"),
		Description: i18n.T("pages.experiments.rules_desc"),
		Width:       tokens.Space12 * 16,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				d.rulesOpen = false
			}
			if v.Button(i18n.T("common.save"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  busy,
				Disabled: busy,
			}).Clicked() {
				d.saveRules(a, projectID, exp)
			}
		},
	}, func() {
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).Wrap().AlignItems(ui.Start).Children(func() {
			ui.Row(v.C).Grow(1).MinWidth(tokens.Space12 * 10).Children(func() {
				v.Textarea(&d.rulesInclude, components.FieldOptions{
					Label:       i18n.T("pages.experiments.rules_include"),
					Description: i18n.T("pages.experiments.rules_lines_hint"),
				})
			})
			ui.Row(v.C).Grow(1).MinWidth(tokens.Space12 * 10).Children(func() {
				v.Textarea(&d.rulesExclude, components.FieldOptions{
					Label:       i18n.T("pages.experiments.rules_exclude"),
					Description: i18n.T("pages.experiments.rules_lines_hint"),
				})
			})
		})
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).Wrap().AlignItems(ui.End).Children(func() {
			ui.Row(v.C).Grow(1).MinWidth(tokens.Space12 * 6).Children(func() {
				v.Input(&d.rulesMaxFile, components.FieldOptions{Label: i18n.T("pages.experiments.rules_max_file")})
			})
			ui.Row(v.C).Grow(1).MinWidth(tokens.Space12 * 6).Children(func() {
				v.Input(&d.rulesMaxTot, components.FieldOptions{Label: i18n.T("pages.experiments.rules_max_total")})
			})
			ui.Row(v.C).Grow(1).MinWidth(tokens.Space12 * 4).Children(func() {
				v.Input(&d.rulesMaxN, components.FieldOptions{Label: i18n.T("pages.experiments.rules_max_files")})
			})
			ui.Row(v.C).Grow(1).MinWidth(tokens.Space12 * 6).Children(func() {
				v.Input(&d.rulesHash, components.FieldOptions{Label: i18n.T("pages.experiments.rules_hash")})
			})
		})
		ui.Text(v.C, i18n.T("pages.experiments.rules_git_forced")).
			FontSize(tokens.Typography.Caption.Size).TextColor(mygo.Color(v.T.Palette.TextMuted))
		if errText != "" {
			ui.Text(v.C, errText).TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !busy {
		d.rulesOpen = false
	}
}

// saveRules validates the edited rules and writes them.
func (d *ExperimentDetail) saveRules(a *app.App, projectID string, exp *experiments.ExperimentDetail) {
	rules := exp.Experiment.ScanRules
	rules.V = experiments.DefaultScanRules().V
	rules.Include = expSplitLines(d.rulesInclude)
	rules.Exclude = expSplitLines(d.rulesExclude)
	maxFile, err := strconv.ParseInt(strings.TrimSpace(d.rulesMaxFile), 10, 64)
	if err != nil || maxFile <= 0 {
		d.mu.Lock()
		d.rulesErr = i18n.T("pages.experiments.rules_bad_number")
		d.mu.Unlock()
		return
	}
	maxTotal, err := strconv.ParseInt(strings.TrimSpace(d.rulesMaxTot), 10, 64)
	if err != nil || maxTotal <= 0 {
		d.mu.Lock()
		d.rulesErr = i18n.T("pages.experiments.rules_bad_number")
		d.mu.Unlock()
		return
	}
	maxFiles, err := strconv.Atoi(strings.TrimSpace(d.rulesMaxN))
	if err != nil || maxFiles <= 0 {
		d.mu.Lock()
		d.rulesErr = i18n.T("pages.experiments.rules_bad_number")
		d.mu.Unlock()
		return
	}
	hashMax, err := strconv.ParseInt(strings.TrimSpace(d.rulesHash), 10, 64)
	if err != nil || hashMax <= 0 {
		d.mu.Lock()
		d.rulesErr = i18n.T("pages.experiments.rules_bad_number")
		d.mu.Unlock()
		return
	}
	rules.MaxFileBytes = maxFile
	rules.MaxTotalBytes = maxTotal
	rules.MaxFiles = maxFiles
	rules.HashMaxBytes = hashMax
	if err := rules.Validate(); err != nil {
		d.mu.Lock()
		d.rulesErr = expErrText(err)
		d.mu.Unlock()
		return
	}
	d.mu.Lock()
	if d.busy["rules"] {
		d.mu.Unlock()
		return
	}
	d.busy["rules"] = true
	d.rulesErr = ""
	d.mu.Unlock()
	api := d.API
	a.RunTask(i18n.T("pages.experiments.task_rules"), func(ctx context.Context, t *app.Task) error {
		err := api.SetScanRules(ctx, projectID, exp.Experiment.ID, rules)
		d.mu.Lock()
		d.busy["rules"] = false
		if err != nil {
			d.rulesErr = expErrText(err)
		}
		d.mu.Unlock()
		if err != nil {
			a.Notify()
			return nil
		}
		d.rulesOpen = false
		d.invalidate()
		a.Toast(app.LevelSuccess, i18n.T("pages.experiments.toast_rules_saved"), "")
		a.Notify()
		return nil
	})
}

// expSplitLines turns a textarea into the line list the rules use.
func expSplitLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
