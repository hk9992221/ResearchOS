package pages

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/schema"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// Trash is the recycle bin page of §16.4.
type Trash struct {
	Services *Services

	mu       sync.Mutex
	loading  bool
	loaded   bool
	items    []TrashItem
	loadErr  error
	newFirst bool

	filter   string
	state    ui.ListState
	selected ui.Selection[string]

	confirmOpen  bool
	confirmPurge bool
	scope        confirm.Scope
	pending      []TrashRef
}

// Page renders the recycle bin.
func (s *Trash) Page(v *components.UI, r ports.Route) {
	if s.Services.App == nil {
		return
	}
	projectID := ""
	if p := s.Services.App.Project(); p != nil {
		projectID = p.ID
	}
	if projectID == "" {
		v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth, Children: func() {
			v.PageHeader(components.PageHeaderOptions{Title: "Recycle Bin"})
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Trash,
				Title:  "No project is open",
				Reason: "The recycle bin belongs to a project. Open a project to see its deleted items.",
			})
		}})
		return
	}
	if s.filter == "" {
		s.filter = "All types"
	}
	s.load(projectID)
	s.mu.Lock()
	items, loaded, loading, loadErr := append([]TrashItem(nil), s.items...), s.loaded, s.loading, s.loadErr
	s.mu.Unlock()

	visible := filterTrash(items, s.filter)
	sortTrash(visible, s.newFirst)

	v.PageContainer(components.PageContainerOptions{MaxWidth: components.ReadingWidth * 1.4, Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       "Recycle Bin",
			Description: "Deleted items stay here until they are restored or permanently removed.",
		})
		if loadErr != nil {
			v.Banner(components.BannerOptions{Tone: components.Danger, Title: "The recycle bin could not be loaded", Message: loadErr.Error()})
		}
		v.Toolbar("Recycle bin actions", func() {
			v.Select(&s.filter, components.SelectOptions{
				Label:   "Type filter",
				Compact: true,
				Width:   tokens.Space10 * 4,
				Options: []string{"All types", "Notes", "Papers", "Ideas", "Experiments", "Tasks"},
			})
			v.Spacer()
			refs := s.selectedRefs(visible)
			v.Button("Restore", components.ButtonOptions{
				Icon:     icons.Refresh,
				Disabled: len(refs) == 0,
				OnClick:  func() { s.restore(projectID, refs) },
			})
			v.Button("Delete permanently", components.ButtonOptions{
				Variant:  components.VariantDanger,
				Icon:     icons.Trash,
				Disabled: len(refs) == 0,
				OnClick:  func() { s.askPurge(projectID, refs) },
			})
			v.Button("Empty bin", components.ButtonOptions{
				Variant:  components.VariantDanger,
				Disabled: len(items) == 0,
				OnClick:  func() { s.askEmpty(projectID) },
			})
		})

		switch {
		case loading && !loaded:
			v.Skeleton(components.SkeletonOptions{Lines: 6})
		case len(items) == 0:
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Trash,
				Title:  "The recycle bin is empty",
				Reason: "Deleted notes, ideas, experiments and tasks appear here and can be restored.",
			})
		case len(visible) == 0:
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Search,
				Title:  "No items of this type",
				Reason: "Choose another type filter to see the deleted items of this project.",
			})
		default:
			cols := []components.Column{
				{Title: "Type", Width: 110},
				{Title: "Title", Sortable: true},
				{Title: "Deleted", Width: 180},
				{Title: "State", Width: 170},
			}
			s.state.Key = func(i int) any { return trashKey(visible[i]) }
			s.state.Selection = &s.selected
			v.Table(components.TableOptions{
				Columns:   cols,
				Rows:      len(visible),
				State:     &s.state,
				Selection: &s.selected,
				Key:       func(i int) any { return trashKey(visible[i]) },
				Cell: func(row, col int) {
					item := visible[row]
					switch col {
					case 0:
						ui.Text(v.C, typeLabel(item.Type))
					case 1:
						ui.Text(v.C, item.Title).SingleLine()
					case 2:
						ui.Text(v.C, item.DeletedAt.Format("2006-01-02 15:04")).FontFeatures("tnum")
					case 3:
						if item.FileMissing {
							v.StatusIndicator(components.Warning, "File missing")
						} else {
							v.StatusIndicator(components.Neutral, "Recoverable")
						}
					}
				},
			}).Grow(1)
		}
		ui.Text(v.C, "Selected items can be restored. Permanent deletion and emptying the bin ask for confirmation and cannot be undone.").
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextSecondary))
	}})

	s.confirm(v, projectID)
}

// confirm renders the destructive confirmation dialog for the pending action.
func (s *Trash) confirm(v *components.UI, projectID string) {
	open := s.confirmOpen
	if !open {
		return
	}
	title := "Permanently delete items?"
	message := fmt.Sprintf("%d items will be removed with their files. This cannot be undone.", len(s.pending))
	label := "Yes, delete permanently"
	if !s.confirmPurge {
		title = "Empty the recycle bin?"
		message = "Every deleted item of this project will be removed with its files. This cannot be undone."
		label = "Yes, empty the bin"
	}
	dialog := v.ConfirmDialog(components.ConfirmOptions{
		Open:         &s.confirmOpen,
		Title:        title,
		Message:      message,
		ConfirmLabel: label,
		Destructive:  true,
		Service:      s.Services.Confirm,
		Scope:        s.scope,
		OnConfirmed: func(tok confirm.Token) {
			s.runTokenAction(projectID, tok)
		},
	})
	_ = dialog
}

// load fetches the bin once.
func (s *Trash) load(projectID string) {
	s.mu.Lock()
	if s.loading || s.loaded {
		s.mu.Unlock()
		return
	}
	s.loading = true
	s.mu.Unlock()
	s.Services.App.RunTask("Load recycle bin", func(ctx context.Context, t *app.Task) error {
		items, err := s.Services.Trash.List(ctx, projectID)
		s.mu.Lock()
		s.loading = false
		s.loaded = true
		if err != nil {
			s.loadErr = err
		} else {
			s.items = items
			s.selected.Clear()
		}
		s.mu.Unlock()
		return err
	})
}

// reload starts a fresh load.
func (s *Trash) reload(projectID string) {
	s.mu.Lock()
	s.loaded = false
	s.mu.Unlock()
	s.load(projectID)
}

// selectedRefs returns the selected refs among the visible rows.
func (s *Trash) selectedRefs(visible []TrashItem) []TrashRef {
	var refs []TrashRef
	for _, item := range visible {
		if s.selected.Has(trashKey(item)) {
			refs = append(refs, TrashRef{Type: item.Type, ID: item.ID})
		}
	}
	return refs
}

// restore restores the selected refs.
func (s *Trash) restore(projectID string, refs []TrashRef) {
	s.Services.App.RunTask("Restore from recycle bin", func(ctx context.Context, t *app.Task) error {
		if err := s.Services.Trash.Restore(ctx, projectID, refs); err != nil {
			return err
		}
		s.Services.App.Toast(app.LevelSuccess, "Items restored", fmt.Sprintf("%d items were restored.", len(refs)))
		s.reload(projectID)
		return nil
	})
}

// askPurge opens the confirmation for permanent deletion.
func (s *Trash) askPurge(projectID string, refs []TrashRef) {
	if len(refs) == 0 {
		return
	}
	s.scope = confirm.Scope{Kind: confirm.KindPurgeEntity, ProjectID: projectID}
	types := make([]string, 0, len(refs))
	ids := make([]string, 0, len(refs))
	for _, r := range refs {
		types = append(types, r.Type)
		ids = append(ids, r.ID)
	}
	s.pending = refs
	s.confirmPurge = true
	s.scope.Kind = confirm.KindPurgeEntity
	s.scope.ProjectID = projectID
	s.scope.EntityTypes = types
	s.scope.EntityIDs = ids
	s.confirmOpen = true
	s.Services.App.Notify()
}

// askEmpty opens the confirmation for emptying the whole bin.
func (s *Trash) askEmpty(projectID string) {
	s.pending = nil
	s.confirmPurge = false
	s.scope = confirm.Scope{Kind: confirm.KindEmptyTrash, ProjectID: projectID}
	s.confirmOpen = true
	s.Services.App.Navigate(s.Services.App.Route())
}

// runTokenAction purges with the token the dialog issued.
func (s *Trash) runTokenAction(projectID string, tok confirm.Token) {
	refs := append([]TrashRef(nil), s.pending...)
	empty := !s.confirmPurge
	s.Services.App.RunTask("Permanently delete", func(ctx context.Context, t *app.Task) error {
		if empty {
			n, err := s.Services.Trash.Empty(ctx, projectID, tok)
			if err != nil {
				return err
			}
			s.Services.App.Toast(app.LevelSuccess, "Recycle bin emptied", fmt.Sprintf("%d items were permanently deleted.", n))
		} else {
			if err := s.Services.Trash.Purge(ctx, projectID, refs, tok); err != nil {
				return err
			}
			s.Services.App.Toast(app.LevelSuccess, "Items permanently deleted", fmt.Sprintf("%d items were removed.", len(refs)))
		}
		s.mu.Lock()
		s.pending = nil
		s.selected.Clear()
		s.mu.Unlock()
		s.reload(projectID)
		return nil
	})
}

// trashKey identifies a trash row across frames.
func trashKey(item TrashItem) string { return item.Type + ":" + item.ID }

// filterTrash keeps the rows of the chosen type filter.
func filterTrash(items []TrashItem, filter string) []TrashItem {
	if filter == "" || strings.HasPrefix(filter, "All") {
		return append([]TrashItem(nil), items...)
	}
	typ := filterType(filter)
	out := make([]TrashItem, 0, len(items))
	for _, item := range items {
		if item.Type == typ {
			out = append(out, item)
		}
	}
	return out
}

// filterType maps a filter label to an entity type.
func filterType(label string) string {
	switch label {
	case "Notes":
		return string(schema.EntityNote)
	case "Papers":
		return string(schema.EntityPaper)
	case "Ideas":
		return string(schema.EntityIdea)
	case "Experiments":
		return string(schema.EntityExperiment)
	case "Tasks":
		return string(schema.EntityTask)
	}
	return ""
}

// sortTrash orders by deletion time.
func sortTrash(items []TrashItem, oldestFirst bool) {
	sort.SliceStable(items, func(i, j int) bool {
		if oldestFirst {
			return items[i].DeletedAt.Before(items[j].DeletedAt)
		}
		return items[i].DeletedAt.After(items[j].DeletedAt)
	})
}

// typeLabel names an entity type in the table.
func typeLabel(t string) string {
	switch t {
	case "note":
		return "Note"
	case "paper":
		return "Paper"
	case "idea":
		return "Idea"
	case "experiment":
		return "Experiment"
	case "task":
		return "Task"
	case "run":
		return "Run"
	}
	return t
}
