package components_test

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/theme"
	"researchos/internal/ui/tokens"
)

func TestEmptyStateExplainsAndActs(t *testing.T) {
	clicked := false
	tt, _ := tester(t, 700, 420, func(u *components.UI) {
		u.EmptyState(components.EmptyStateOptions{
			Icon:        icons.Literature,
			Title:       "No papers yet",
			Reason:      "This project has no papers linked. Import a PDF or connect Zotero.",
			ActionLabel: "Import paper",
			OnAction:    func() { clicked = true },
		})
	})
	for _, want := range []string{"No papers yet", "This project has no papers linked", "Import paper"} {
		if !tt.HasText(want) {
			t.Errorf("empty state lacks %q; texts %q", want, tt.Texts())
		}
	}
	prev := clicked
	click(t, tt, "Import paper")
	if clicked == prev {
		t.Error("empty state action did not run")
	}
}

func TestErrorStateIsReadable(t *testing.T) {
	tt, _ := tester(t, 700, 460, func(u *components.UI) {
		u.ErrorState(components.ErrorStateOptions{
			Title:      "The workspace could not be read",
			Cause:      "The marker file .researchos is missing.",
			Suggestion: "Choose the workspace folder again, or initialize a new one.",
			Code:       "workspace.marker_missing",
			RetryLabel: "Try again",
		})
	})
	for _, want := range []string{
		"The workspace could not be read",
		"The marker file .researchos is missing.",
		"Choose the workspace folder again",
		"workspace.marker_missing",
		"Try again",
	} {
		if !tt.HasText(want) {
			t.Errorf("error state lacks %q; texts %q", want, tt.Texts())
		}
	}
}

func TestBannerCarriesTextNotOnlyColor(t *testing.T) {
	ran := false
	tt, _ := tester(t, 760, 260, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PagePadding).Children(func() {
			u.Banner(components.BannerOptions{
				Tone:        components.Warning,
				Title:       "Experiment folder not found",
				Message:     "The folder was moved or deleted. Relocate it to keep the run history.",
				ActionLabel: "Relocate",
				OnAction:    func() { ran = true },
			})
		})
	})
	for _, want := range []string{"Experiment folder not found", "Relocate it to keep", "Relocate"} {
		if !tt.HasText(want) {
			t.Errorf("banner lacks %q; texts %q", want, tt.Texts())
		}
	}
	click(t, tt, "Relocate")
	if !ran {
		t.Error("banner action did not run")
	}
}

func TestStatusIndicatorAlwaysNamesItsState(t *testing.T) {
	tt, _ := tester(t, 420, 200, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PageBlockGap).Gap(tokens.Space3).Children(func() {
			u.StatusIndicator(components.Danger, "")
			u.StatusIndicator(components.Success, "Healthy")
			u.Badge("3 missing", components.Warning)
		})
	})
	if !tt.HasText("Error") {
		t.Errorf("status with no text did not name its state; texts %q", tt.Texts())
	}
	if !tt.HasText("Healthy") || !tt.HasText("3 missing") {
		t.Errorf("status text missing; texts %q", tt.Texts())
	}
}

func TestProgressShowsValueAndLabel(t *testing.T) {
	tt, _ := tester(t, 420, 200, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PageBlockGap).Gap(tokens.Space3).Children(func() {
			u.Progress(components.ProgressOptions{Value: 0.4, Label: "Scanning files"})
			u.Progress(components.ProgressOptions{Value: -1, Label: "Connecting"})
		})
	})
	if !tt.HasText("Scanning files") || !tt.HasText("Connecting") {
		t.Fatalf("progress labels missing; texts %q", tt.Texts())
	}
	info, ok := tt.Access("Scanning files")
	if !ok {
		t.Fatalf("progress bar has no accessible name")
	}
	if info.Now < 0.39 || info.Now > 0.41 {
		t.Errorf("progress value = %v, want 0.4", info.Now)
	}
}

func TestSkeletonHasNoText(t *testing.T) {
	tt, _ := tester(t, 420, 260, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PageBlockGap).Gap(tokens.Space3).Children(func() {
			u.Skeleton(components.SkeletonOptions{Lines: 3})
			u.Skeleton(components.SkeletonOptions{Width: 120, Height: 24})
		})
	})
	if len(tt.Texts()) != 0 {
		t.Errorf("skeleton shows text: %q", tt.Texts())
	}
}

func TestCardShowsTitleDescriptionAndBody(t *testing.T) {
	tt, _ := tester(t, 600, 320, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PagePadding).Children(func() {
			u.Card(components.CardOptions{Title: "Project statistics", Description: "Last 30 days"}, func() {
				ui.Text(u.C, "12 notes")
			})
		})
	})
	for _, want := range []string{"Project statistics", "Last 30 days", "12 notes"} {
		if !tt.HasText(want) {
			t.Errorf("card lacks %q; texts %q", want, tt.Texts())
		}
	}
}

// TestCardFollowsTheTheme checks AC-UI-04: components repaint from tokens in
// Dark as they do in Light, without structural changes.
func TestCardFollowsTheTheme(t *testing.T) {
	tt, p := tester(t, 600, 320, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PagePadding).Children(func() {
			u.Card(components.CardOptions{Title: "Project statistics"}, func() {
				ui.Text(u.C, "12 notes")
			})
		})
	})
	r, ok := tt.Find("Project statistics")
	if !ok {
		t.Fatalf("card title missing; texts %q", tt.Texts())
	}
	wantColor(t, "light card", at(tt.Image(), r.X-8, r.Y+4), tokens.Light().BgSurface, 3)
	if err := p.SetMode(context.Background(), theme.Dark); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	wantColor(t, "dark card", at(tt.Image(), r.X-8, r.Y+4), tokens.Dark().BgSurface, 3)
	if !tt.HasText("12 notes") {
		t.Error("theme switch lost card content")
	}
}
