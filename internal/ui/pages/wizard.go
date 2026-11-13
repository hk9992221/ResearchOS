package pages

import (
	"context"

	"github.com/egoist/mygo/ui"

	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
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
			Title:       "Set up ResearchOS",
			Description: "A workspace holds your databases, papers, projects and exports in one folder.",
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
		w.errText = snap.Err.Error()
		return
	}
	w.step = stepDone
	w.errText = ""
}

func (w *Wizard) welcome(v *components.UI) {
	p := v.T.Palette
	v.Card(components.CardOptions{Title: "What happens next"}, func() {
		ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
			for _, line := range []string{
				"Choose a folder. An empty folder is initialized; an existing workspace is opened as it is.",
				"ResearchOS writes its databases and libraries only inside that folder.",
				"Nothing outside the folder is created or moved.",
			} {
				ui.Row(v.C).Gap(tokens.ComponentGap).AlignItems(ui.Start).Children(func() {
					ui.Text(v.C, "•").TextColor(mygoColor(p.AccentDefault))
					ui.Text(v.C, line).FontSize(tokens.Typography.Body.Size)
				})
			}
			v.Button("Get started", components.ButtonOptions{
				Variant: components.VariantPrimary,
				OnClick: func() { w.step = stepDirectory },
			})
		})
	})
}

func (w *Wizard) directory(v *components.UI) {
	if w.errText != "" {
		v.Banner(components.BannerOptions{Tone: components.Danger, Title: "The folder cannot be used", Message: w.errText})
	}
	v.Card(components.CardOptions{Title: "Workspace folder"}, func() {
		ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
			in := v.Input(&w.path, components.FieldOptions{
				Label:       "Folder path",
				Description: "Pick a folder with the system dialog, or type its path and validate it.",
				Placeholder: "F:/ResearchOS-Workspace",
			})
			if in.Changed() {
				w.insp = nil
			}
			ui.Row(v.C).Gap(tokens.ComponentGap).Children(func() {
				v.Button("Browse…", components.ButtonOptions{
					Variant: components.VariantSecondary,
					Icon:    icons.FolderOpen,
					OnClick: func() { w.browse() },
				})
				v.Button("Validate folder", components.ButtonOptions{
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
		v.Banner(components.BannerOptions{Tone: components.Danger, Title: "Setup failed", Message: w.errText})
	}
	v.Card(components.CardOptions{Title: "Folder check", Description: insp.Root}, func() {
		ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
			v.StatusIndicator(setupTone(insp.State), setupLabel(insp.State))
			if insp.Message != "" {
				ui.Text(v.C, insp.Message).FontSize(tokens.Typography.Secondary.Size)
			}
			ui.Row(v.C).Gap(tokens.ComponentGap).Children(func() {
				switch insp.State {
				case SetupEmpty:
					v.Button("Initialize workspace", components.ButtonOptions{
						Variant: components.VariantPrimary,
						OnClick: func() {
							w.start("Initialize workspace", func(ctx context.Context) error { return w.Services.Setup.Initialize(ctx, w.path) })
						},
					})
				case SetupExisting:
					v.Button("Open workspace", components.ButtonOptions{
						Variant: components.VariantPrimary,
						OnClick: func() {
							w.start("Open workspace", func(ctx context.Context) error { return w.Services.Setup.Open(ctx, w.path) })
						},
					})
				}
				v.Button("Choose another folder", components.ButtonOptions{
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
	stage := "Working"
	if w.task != nil {
		if snap := w.task.Snapshot(); snap.Stage != "" {
			stage = snap.Stage
		}
	}
	v.Card(components.CardOptions{Title: "Setting up the workspace"}, func() {
		ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
			ui.Text(v.C, stage).FontSize(tokens.Typography.Body.Size)
			v.Progress(components.ProgressOptions{Value: -1, Label: stage})
		})
	})
}

func (w *Wizard) done(v *components.UI) {
	v.Banner(components.BannerOptions{
		Tone:    components.Success,
		Title:   "Workspace ready",
		Message: "ResearchOS opened the workspace. The shell and your projects are available now.",
	})
	if w.path != "" {
		v.Card(components.CardOptions{Title: "Workspace"}, func() {
			ui.Text(v.C, w.path).Font(tokens.SystemFonts.Mono)
		})
	}
}

// browse opens the native folder picker and validates the choice.
func (w *Wizard) browse() {
	if w.Services.ChooseFolder == nil {
		w.errText = "The system folder picker is unavailable; type the path instead."
		return
	}
	chosen, err := w.Services.ChooseFolder()
	if err != nil {
		w.errText = "The folder chooser failed: " + err.Error()
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
		w.errText = err.Error()
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
		w.errText = "task runner unavailable"
		return
	}
	w.step = stepRunning
	w.errText = ""
	w.task = w.Services.App.RunTask(name, func(ctx context.Context, t *app.Task) error {
		t.Stage("Preparing")
		return fn(ctx)
	})
}

// setupLabel names an inspection result.
func setupLabel(s SetupState) string {
	switch s {
	case SetupExisting:
		return "Existing workspace"
	case SetupMarkerMissing:
		return "Marker file missing"
	case SetupMarkerCorrupt:
		return "Marker file damaged"
	case SetupForeign:
		return "Folder is not empty"
	}
	return "New workspace"
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
		return "The folder contains a global database but its .researchos marker is gone. Restore the marker from a backup, or choose the folder that holds the complete workspace."
	case SetupMarkerCorrupt:
		return "The .researchos marker cannot be read. Restore it from a backup, or choose another workspace."
	case SetupForeign:
		return "ResearchOS never initializes into a non-empty folder. Choose an empty folder or an existing workspace."
	}
	return ""
}

// mygoColor is a small local alias so the wizard can tint its bullet without
// importing the adapter at every call site.
func mygoColor(c tokens.Color) ui.Color { return ui.RGBA(c.R, c.G, c.B, float32(c.A)/255) }
