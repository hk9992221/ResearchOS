package icons_test

import (
	"testing"

	"researchos/internal/ui/icons"
)

func TestRegistry(t *testing.T) {
	all := icons.All()
	if len(all) < 30 {
		t.Fatalf("registry has %d icons, want the system set", len(all))
	}
	seen := make(map[string]bool, len(all))
	for _, ic := range all {
		if ic.Name() == "" {
			t.Error("icon without a name")
		}
		if ic.SVG() == nil {
			t.Errorf("icon %q has no parsed SVG", ic.Name())
		}
		if seen[ic.Name()] {
			t.Errorf("duplicate icon %q", ic.Name())
		}
		seen[ic.Name()] = true
	}
	for _, want := range []string{
		"house", "folder", "inbox", "store", "settings", "circle-help",
		"book-open", "notebook-pen", "lightbulb", "flask-conical",
		"square-kanban", "calendar-days", "search", "command", "trash-2",
		"refresh-cw", "scan-line", "stethoscope", "chevron-down", "x",
		"check", "triangle-alert", "info", "plus", "external-link", "copy",
	} {
		if !seen[want] {
			t.Errorf("registry lacks %q", want)
		}
	}
}

func TestSizes(t *testing.T) {
	if icons.SizeDefault != 16 || icons.SizeNav != 18 || icons.SizeAction != 20 {
		t.Error("icon sizes drifted from ui_plan.md §5")
	}
}
