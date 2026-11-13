package components_test

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"researchos/internal/ui/components"
	"researchos/internal/ui/tokens"
)

func TestInputErrorMarksTheControl(t *testing.T) {
	value := ""
	tt, _ := tester(t, 640, 360, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PagePadding).Children(func() {
			u.Input(&value, components.FieldOptions{
				Label:       "Workspace path",
				Description: "The folder that holds the database.",
				Error:       "The folder does not exist.",
				Placeholder: "F:/ResearchOS",
			})
		})
	})
	if !tt.HasText("The folder does not exist.") {
		t.Fatalf("error text missing; texts %q", tt.Texts())
	}
	info, ok := tt.AccessByRole(ui.RoleTextField, "Workspace path")
	if !ok {
		t.Fatalf("input has no accessible label; texts %q", tt.Texts())
	}
	if !info.Invalid {
		t.Error("input is not marked invalid")
	}
	if len(info.ErrorMessage) == 0 && !strings.Contains(info.Description, "The folder does not exist.") {
		t.Errorf("the error is not related to the input for assistive technology: %+v", info)
	}
}

func TestInputAcceptsTyping(t *testing.T) {
	value := ""
	tt, _ := tester(t, 640, 300, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PagePadding).Children(func() {
			u.Input(&value, components.FieldOptions{Label: "Project name"})
		})
	})
	click(t, tt, "Project name")
	tt.Type("Alpha")
	if value != "Alpha" {
		t.Fatalf("value = %q, want Alpha", value)
	}
}

func TestCheckboxAndSwitchToggle(t *testing.T) {
	var check, on bool
	tt, _ := tester(t, 640, 360, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PagePadding).Gap(tokens.Space3).Children(func() {
			u.Checkbox(&check, "Include archived", components.ToggleOptions{Description: "Shows deleted notes too."})
			u.Switch(&on, "Enable AI", components.ToggleOptions{})
		})
	})
	if !tt.HasText("Shows deleted notes too.") {
		t.Errorf("description missing; texts %q", tt.Texts())
	}
	tt.Click("Include archived")
	if !check {
		t.Error("check box did not toggle")
	}
	info, ok := tt.Access("Enable AI")
	if !ok {
		t.Fatalf("switch has no accessible name; texts %q", tt.Texts())
	}
	tt.ClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
	if !on {
		t.Error("switch did not toggle")
	}
}

func TestRadioGroupChooses(t *testing.T) {
	choice := "day"
	tt, _ := tester(t, 640, 300, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PagePadding).Children(func() {
			u.RadioGroup(&choice, []components.RadioOption[string]{
				{Value: "day", Label: "Day"},
				{Value: "week", Label: "Week"},
				{Value: "month", Label: "Month"},
			}, components.RadioOptions{Label: "Range", Row: true})
		})
	})
	tt.Click("Week")
	if choice != "week" {
		t.Errorf("choice = %q, want week", choice)
	}
	if !tt.HasText("Day") || !tt.HasText("Month") {
		t.Errorf("radio labels missing; texts %q", tt.Texts())
	}
}

func TestSelectChoosesAnOption(t *testing.T) {
	value := "Free"
	tt, _ := tester(t, 640, 420, func(u *components.UI) {
		ui.Column(u.C).Padding(tokens.PagePadding).Children(func() {
			u.Select(&value, components.SelectOptions{Label: "Plan", Options: []string{"Free", "Pro", "Team"}})
		})
	})
	info, ok := tt.AccessByRole(ui.RolePopUpButton, "Plan")
	if !ok {
		t.Fatalf("select has no accessible label; texts %q", tt.Texts())
	}
	tt.ClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
	tt.Frame()
	if err := tt.Click("Team"); err != nil {
		t.Fatalf("click option: %v; texts %q", err, tt.Texts())
	}
	if value != "Team" {
		t.Fatalf("value = %q, want Team", value)
	}
}
