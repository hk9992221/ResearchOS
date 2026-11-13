package pages

import (
	"context"

	"github.com/egoist/mygo/ui"

	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/tokens"
)

// Wizard is the first-run wizard of §16.1: Welcome → Select directory →
// Validate → Initialize → Completion.
type Wizard struct {
	Services *Services

	step    wizardStep
	path    string
	insp    *SetupInspection
	errText string
	task    *app.Task
}

type wizardStep int

const (
	stepWelcome wizardStep = iota
	stepDirectory
	stepReview
	stepRunning
	stepDone
)

// Page renders the wizard step for this frame.
func (w *Wizard) Page(v *components.UI, r ports.Route) {
	if w.path == "" && w.Services.Setup != nil {
		w.path = w.Services.Setup.CurrentRoot()
	}
	w.settleTask()
	v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth * 1.2, Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.wizard.title"),
			Description: i18n.T("pages.wizard.description"),
		})
		switch w.step {
		case stepWelcome:
			w.welcome(v)
		case stepDirectory:
			w.directory(v)
		case stepReview:
			w.review(v)
		case stepRunning:
			w.running(v)
		case stepDone:
			w.done(v)
		}
	}})
}

// settleTask advances the wizard when the running task finished.
func (w *Wizard) settleTask() {
	if w.step != stepRunning || w.task == nil {
		return
	}
	snap := w.task.Snapshot()
	if !snap.Finished {
		return
	}
	if snap.Err != nil {
		w.step = stepReview
		w.errText = errtext.Text(snap.Err)
		return
	}
	w.step = stepDone
	w.errText = ""
}

func (w *Wizard) welcome(v *components.UI) {
	p := v.T.Palette
	v.Card(components.CardOptions{Title: i18n.T("pages.wizard.card_next")}, func() {
		ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
			for _, key := range []string{
				"pages.wizard.bullet_1",
				"pages.wizard.bullet_2",
				"pages.wizard.bullet_3",
			} {
				line := i18n.T(key)
				ui.Row(v.C).Gap(tokens.ComponentGap).AlignItems(ui.Start).Children(func() {
					ui.Text(v.C, i18n.T("pages.wizard.bullet_glyph")).TextColor(mygoColor(p.AccentDefault))
					ui.Text(v.C, line).FontSize(tokens.Typography.Body.Size)
				})
			}
			v.Button(i18n.T("pages.wizard.btn_start"), components.ButtonOptions{
				Variant: components.VariantPrimary,
				OnClick: func() { w.step = stepDirectory },
			})
		})
	})
}

func (w *Wizard) directory(v *components.UI) {
	if w.errText != "" {
		v.Banner(components.BannerOptions{Tone: components.Danger, Title: i18n.T("pages.wizard.banner_folder_unusable"), Message: w.errText})
	}
	v.Card(components.CardOptions{Title: i18n.T("pages.wizard.card_folder")}, func() {
		ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
			in := v.Input(&w.path, components.FieldOptions{
				Label:       i18n.T("pages.wizard.field_path_label"),
				Description: i18n.T("pages.wizard.field_path_desc"),
				Placeholder: "F:/ResearchOS-Workspace",
			})
			if in.Changed() {
				w.insp = nil
			}
			ui.Row(v.C).Gap(tokens.ComponentGap).Children(func() {
				v.Button(i18n.T("pages.common.btn_browse"), components.ButtonOptions{
					Variant: components.VariantSecondary,
					Icon:    icons.FolderOpen,
					OnClick: func() { w.browse() },
				})
				v.Button(i18n.T("pages.wizard.btn_validate"), components.ButtonOptions{
					Variant:  components.VariantPrimary,
					Disabled: w.path == "",
					OnClick:  func() { w.inspect() },
				})
			})
		})
	})
}

func (w *Wizard) review(v *components.UI) {
	insp := w.insp
	if insp == nil {
		w.step = stepDirectory
		return
	}
	if w.errText != "" {
		v.Banner(components.BannerOptions{Tone: components.Danger, Title: i18n.T("pages.wizard.banner_setup_failed"), Message: w.errText})
	}
	v.Card(components.CardOptions{Title: i18n.T("pages.wizard.card_check"), Description: insp.Root}, func() {
		ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
			v.StatusIndicator(setupTone(insp.State), setupLabel(insp.State))
			if insp.Message != "" {
				ui.Text(v.C, insp.Message).FontSize(tokens.Typography.Secondary.Size)
			}
			ui.Row(v.C).Gap(tokens.ComponentGap).Children(func() {
				switch insp.State {
				case SetupEmpty:
					v.Button(i18n.T("pages.wizard.btn_initialize"), components.ButtonOptions{
						Variant: components.VariantPrimary,
						OnClick: func() {
							w.start(i18n.T("pages.wizard.btn_initialize"), func(ctx context.Context) error { return w.Services.Setup.Initialize(ctx, w.path) })
						},
					})
				case SetupExisting:
					v.Button(i18n.T("pages.wizard.btn_open"), components.ButtonOptions{
						Variant: components.VariantPrimary,
						OnClick: func() {
							w.start(i18n.T("pages.wizard.btn_open"), func(ctx context.Context) error { return w.Services.Setup.Open(ctx, w.path) })
						},
					})
				}
				v.Button(i18n.T("pages.wizard.btn_choose_other"), components.ButtonOptions{
					Variant: components.VariantSecondary,
					OnClick: func() {
						w.insp = nil
						w.errText = ""
						w.step = stepDirectory
					},
				})
			})
			if insp.State == SetupMarkerMissing || insp.State == SetupMarkerCorrupt || insp.State == SetupForeign {
				ui.Text(v.C, setupGuidance(insp.State)).FontSize(tokens.Typography.Caption.Size)
			}
		})
	})
}

func (w *Wizard) running(v *components.UI) {
	stage := i18n.T("pages.wizard.stage_working")
	if w.task != nil {
		if snap := w.task.Snapshot(); snap.Stage != "" {
			stage = snap.Stage
		}
	}
	v.Card(components.CardOptions{Title: i18n.T("pages.wizard.card_running")}, func() {
		ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
			ui.Text(v.C, stage).FontSize(tokens.Typography.Body.Size)
			v.Progress(components.ProgressOptions{Value: -1, Label: stage})
		})
	})
}

func (w *Wizard) done(v *components.UI) {
	v.Banner(components.BannerOptions{
		Tone:    components.Success,
		Title:   i18n.T("pages.wizard.banner_done"),
		Message: i18n.T("pages.wizard.banner_done_message"),
	})
	if w.path != "" {
		v.Card(components.CardOptions{Title: i18n.T("pages.wizard.card_workspace_path")}, func() {
			ui.Text(v.C, w.path).Font(tokens.SystemFonts.Mono)
		})
	}
}

// browse opens the native folder picker and validates the choice.
func (w *Wizard) browse() {
	if w.Services.ChooseFolder == nil {
		w.errText = i18n.T("pages.wizard.err_picker_unavailable")
		return
	}
	chosen, err := w.Services.ChooseFolder()
	if err != nil {
		w.errText = i18n.Tf("pages.wizard.err_chooser_failed", map[string]string{"error": errtext.Text(err)})
		return
	}
	if chosen == "" {
		return
	}
	w.path = chosen
	w.inspect()
}

// inspect validates the typed path synchronously: it only stats and opens the
// folder, without creating anything.
func (w *Wizard) inspect() {
	if w.Services.Setup == nil || w.path == "" {
		return
	}
	ctx := context.Background()
	insp, err := w.Services.Setup.Inspect(ctx, w.path)
	if err != nil {
		w.errText = errtext.Text(err)
		w.insp = nil
		w.step = stepDirectory
		return
	}
	w.insp = &insp
	w.errText = ""
	w.step = stepReview
}

// start runs initialization or opening as a managed task, so the wizard never
// blocks the frame and the shell's task indicator shows it.
func (w *Wizard) start(name string, fn func(ctx context.Context) error) {
	if w.Services.App == nil {
		w.errText = i18n.T("pages.wizard.err_no_task_runner")
		return
	}
	w.step = stepRunning
	w.errText = ""
	w.task = w.Services.App.RunTask(name, func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("pages.wizard.stage_preparing"))
		return fn(ctx)
	})
}

// setupLabel names an inspection result.
func setupLabel(s SetupState) string {
	switch s {
	case SetupExisting:
		return i18n.T("pages.wizard.state_existing")
	case SetupMarkerMissing:
		return i18n.T("pages.wizard.state_marker_missing")
	case SetupMarkerCorrupt:
		return i18n.T("pages.wizard.state_marker_corrupt")
	case SetupForeign:
		return i18n.T("pages.wizard.state_foreign")
	}
	return i18n.T("pages.wizard.state_empty")
}

// setupTone colors an inspection result.
func setupTone(s SetupState) components.Tone {
	switch s {
	case SetupExisting:
		return components.Success
	case SetupMarkerMissing, SetupMarkerCorrupt, SetupForeign:
		return components.Danger
	}
	return components.Info
}

// setupGuidance explains what the user can do about a refused folder.
func setupGuidance(s SetupState) string {
	switch s {
	case SetupMarkerMissing:
		return i18n.T("pages.wizard.guidance_marker_missing")
	case SetupMarkerCorrupt:
		return i18n.T("pages.wizard.guidance_marker_corrupt")
	case SetupForeign:
		return i18n.T("pages.wizard.guidance_foreign")
	}
	return ""
}

// mygoColor is a small local alias so the wizard can tint its bullet without
// importing the adapter at every call site.
func mygoColor(c tokens.Color) ui.Color { return ui.RGBA(c.R, c.G, c.B, float32(c.A)/255) }
