package components_test

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/confirm"
	"researchos/internal/ui/components"
	"researchos/internal/ui/tokens"
)

func TestDialogEscClosesAndRestoresFocus(t *testing.T) {
	open := false
	tt, _ := tester(t, 760, 520, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PagePadding).Children(func() {
			if u.Button("Delete project", components.ButtonOptions{Variant: components.VariantDanger}).Clicked() {
				open = true
			}
		})
		u.Dialog(&open, components.DialogOptions{
			Title:       "Delete project?",
			Description: "This cannot be undone.",
			Actions: func() {
				if u.Button("Cancel", components.ButtonOptions{}).Clicked() {
					open = false
				}
			},
		}, func() {
			ui.Text(u.C, "All notes and experiments will be removed.")
		})
	})

	click(t, tt, "Delete project")
	if !open {
		t.Fatal("dialog did not open")
	}
	if !tt.HasText("Delete project?") || !tt.HasText("All notes and experiments will be removed.") {
		t.Fatalf("dialog content missing; texts %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if open {
		t.Fatal("Escape did not close the dialog")
	}
	if !tt.Focused("Delete project") {
		t.Errorf("focus did not return to the opening button; texts %q", tt.Texts())
	}
}

func TestConfirmDialogIssuesToken(t *testing.T) {
	svc := confirm.New(confirm.Options{})
	scope := confirm.Scope{
		Kind:        confirm.KindPurgeEntity,
		ProjectID:   "p1",
		EntityTypes: []string{"note"},
		EntityIDs:   []string{"n1"},
	}
	var (
		open bool
		got  confirm.Token
	)
	tt, _ := tester(t, 760, 520, func(u *components.UI) {
		u.Ctx = context.Background()
		ui.Column(u.C).Padding(tokens.PagePadding).Children(func() {
			if u.Button("Delete note", components.ButtonOptions{Variant: components.VariantDanger}).Clicked() {
				open = true
			}
		})
		u.ConfirmDialog(components.ConfirmOptions{
			Open:         &open,
			Title:        "Permanently delete?",
			Message:      "The note and its file will be removed.",
			ConfirmLabel: "Permanently delete",
			Destructive:  true,
			Service:      svc,
			Scope:        scope,
			OnConfirmed:  func(tok confirm.Token) { got = tok },
		})
	})

	click(t, tt, "Delete note")
	if !tt.HasText("Permanently delete?") {
		t.Fatalf("confirmation dialog did not open; texts %q", tt.Texts())
	}
	click(t, tt, "Permanently delete")
	if open {
		t.Error("dialog stayed open after confirmation")
	}
	if got == "" {
		t.Fatal("no confirmation token was issued")
	}
	if err := svc.Consume(context.Background(), got, scope); err != nil {
		t.Fatalf("issued token does not authorize the scope: %v", err)
	}
}

func TestConfirmDialogWithoutServiceRefuses(t *testing.T) {
	open := true
	tt, _ := tester(t, 640, 400, func(u *components.UI) {
		u.ConfirmDialog(components.ConfirmOptions{
			Open:         &open,
			Title:        "Empty trash?",
			ConfirmLabel: "Empty trash",
			Destructive:  true,
		})
	})
	info, ok := tt.AccessByRole(ui.RoleButton, "Empty trash")
	if !ok {
		t.Fatalf("confirm button missing; texts %q", tt.Texts())
	}
	if !info.Disabled {
		t.Error("confirmation is possible without a confirm service")
	}
	if !tt.HasText("Empty trash?") {
		t.Errorf("dialog text missing: %q", tt.Texts())
	}
}

func TestPopoverOpensAndEscapes(t *testing.T) {
	open := false
	tt, _ := tester(t, 640, 420, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PagePadding).Children(func() {
			anchor := u.Button("Filters", components.ButtonOptions{})
			if anchor.Clicked() {
				open = true
			}
			u.Popover(anchor, &open, func() {
				ui.Text(u.C, "Only active projects")
			})
		})
	})
	click(t, tt, "Filters")
	if !open || !tt.HasText("Only active projects") {
		t.Fatalf("popover did not open; open=%v texts=%q", open, tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if open {
		t.Error("Escape did not close the popover")
	}
}
