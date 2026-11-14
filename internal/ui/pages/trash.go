package pages

import (
	"context"
	"sort"
	"strconv"
	"sync"

	"github.com/egoist/mygo/ui"

	"researchos/internal/foundation/confirm"
	"researchos/internal/foundation/schema"
	"researchos/internal/i18n"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/errtext"
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

	// filterKey is the stable filter value (an entity type or "all") and
	// filterLabel the localized option label the select shows. The label is
	// derived from the key every frame, exactly as the language select does:
	// a language switch re-labels the options without resetting the choice.
	filterKey   string
	filterLabel string
	state       ui.ListState
	selected    ui.Selection[string]

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
		v.PageContainer(components.PageContainerOptions{Children: func() {
			v.PageHeader(components.PageHeaderOptions{Title: i18n.T("pages.trash.title")})
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Trash,
				Title:  i18n.T("pages.common.empty_no_project_title"),
				Reason: i18n.T("pages.trash.empty_no_project_reason"),
			})
		}})
		return
	}
	labels, keys := trashFilterChoices()
	if s.filterKey == "" {
		s.filterKey = trashFilterAll
	}
	expected := trashFilterLabel(s.filterKey, labels, keys)
	if s.filterLabel != expected {
		// A label that no longer matches the stored key is the user's new
		// choice, or the old wording of the same choice after a language
		// switch: map it back by index, and keep the key when it maps to
		// nothing.
		if k, ok := trashFilterKeyOf(s.filterLabel, labels, keys); ok {
			s.filterKey = k
			expected = trashFilterLabel(s.filterKey, labels, keys)
		}
		s.filterLabel = expected
	}
	s.load(projectID)
	s.mu.Lock()
	items, loaded, loading, loadErr := append([]TrashItem(nil), s.items...), s.loaded, s.loading, s.loadErr
	s.mu.Unlock()

	visible := filterTrash(items, s.filterKey)
	sortTrash(visible, s.newFirst)

	v.PageContainer(components.PageContainerOptions{Children: func() {
		v.PageHeader(components.PageHeaderOptions{
			Title:       i18n.T("pages.trash.title"),
			Description: i18n.T("pages.trash.description"),
		})
		if loadErr != nil {
			v.Banner(components.BannerOptions{Tone: components.Danger, Title: i18n.T("pages.trash.banner_load_fail"), Message: errtext.Text(loadErr)})
		}
		v.Toolbar(i18n.T("pages.trash.toolbar_label"), func() {
			v.Select(&s.filterLabel, components.SelectOptions{
				Label:   i18n.T("pages.trash.filter_label"),
				Compact: true,
				Width:   tokens.Space10 * 4,
				Options: labels,
			})
			v.Spacer()
			refs := s.selectedRefs(visible)
			v.Button(i18n.T("pages.trash.btn_restore"), components.ButtonOptions{
				Icon:     icons.Refresh,
				Disabled: len(refs) == 0,
				OnClick:  func() { s.restore(projectID, refs) },
			})
			v.Button(i18n.T("pages.trash.btn_delete_permanently"), components.ButtonOptions{
				Variant:  components.VariantDanger,
				Icon:     icons.Trash,
				Disabled: len(refs) == 0,
				OnClick:  func() { s.askPurge(projectID, refs) },
			})
			v.Button(i18n.T("pages.trash.btn_empty"), components.ButtonOptions{
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
				Title:  i18n.T("pages.trash.empty_title"),
				Reason: i18n.T("pages.trash.empty_reason"),
			})
		case len(visible) == 0:
			v.EmptyState(components.EmptyStateOptions{
				Icon:   icons.Search,
				Title:  i18n.T("pages.trash.no_filter_results_title"),
				Reason: i18n.T("pages.trash.no_filter_results_reason"),
			})
		default:
			cols := []components.Column{
				{Title: i18n.T("pages.trash.col_type"), Width: 110},
				{Title: i18n.T("pages.trash.col_title"), Sortable: true},
				{Title: i18n.T("pages.trash.col_deleted"), Width: 180},
				{Title: i18n.T("pages.trash.col_state"), Width: 170},
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
							v.StatusIndicator(components.Warning, i18n.T("pages.trash.status_file_missing"))
						} else {
							v.StatusIndicator(components.Neutral, i18n.T("pages.trash.status_recoverable"))
						}
					}
				},
			}).Grow(1)
		}
		ui.Text(v.C, i18n.T("pages.trash.footnote")).
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
	title := i18n.T("pages.trash.confirm_purge_title")
	message := i18n.Tf("pages.trash.confirm_purge_message", map[string]string{"count": strconv.Itoa(len(s.pending))})
	label := i18n.T("pages.trash.confirm_purge_yes")
	if !s.confirmPurge {
		title = i18n.T("pages.trash.confirm_empty_title")
		message = i18n.T("pages.trash.confirm_empty_message")
		label = i18n.T("pages.trash.confirm_empty_yes")
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
	s.Services.App.RunTask(i18n.T("pages.trash.task_load"), func(ctx context.Context, t *app.Task) error {
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
	s.Services.App.RunTask(i18n.T("pages.trash.task_restore"), func(ctx context.Context, t *app.Task) error {
		if err := s.Services.Trash.Restore(ctx, projectID, refs); err != nil {
			return err
		}
		s.Services.App.Toast(app.LevelSuccess, i18n.T("pages.trash.toast_restored_title"),
			i18n.Tf("pages.trash.toast_restored_message", map[string]string{"count": strconv.Itoa(len(refs))}))
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
	s.Services.App.RunTask(i18n.T("pages.trash.task_purge"), func(ctx context.Context, t *app.Task) error {
		if empty {
			n, err := s.Services.Trash.Empty(ctx, projectID, tok)
			if err != nil {
				return err
			}
			s.Services.App.Toast(app.LevelSuccess, i18n.T("pages.trash.toast_emptied_title"),
				i18n.Tf("pages.trash.toast_emptied_message", map[string]string{"count": strconv.Itoa(n)}))
		} else {
			if err := s.Services.Trash.Purge(ctx, projectID, refs, tok); err != nil {
				return err
			}
			s.Services.App.Toast(app.LevelSuccess, i18n.T("pages.trash.toast_purged_title"),
				i18n.Tf("pages.trash.toast_purged_message", map[string]string{"count": strconv.Itoa(len(refs))}))
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

// trashFilterAll is the stable key of the "all types" filter.
const trashFilterAll = "all"

// trashFilterChoices returns the filter's option labels and the stable keys
// behind them, in order: the "all" choice first, then every entity type the
// bin can hold. The keys never change with the language, as the language
// select's tags do.
func trashFilterChoices() (labels, keys []string) {
	labels = []string{
		i18n.T("pages.trash.filter_all"),
		i18n.T("pages.trash.filter_notes"),
		i18n.T("pages.trash.filter_papers"),
		i18n.T("pages.trash.filter_ideas"),
		i18n.T("pages.trash.filter_experiments"),
		i18n.T("pages.trash.filter_tasks"),
	}
	keys = []string{
		trashFilterAll,
		string(schema.EntityNote),
		string(schema.EntityPaper),
		string(schema.EntityIdea),
		string(schema.EntityExperiment),
		string(schema.EntityTask),
	}
	return labels, keys
}

// trashFilterLabel is the option label of a stable key. A key no option
// names, as a value written by a newer build, falls back to the "all" label.
func trashFilterLabel(key string, labels, keys []string) string {
	for i, k := range keys {
		if k == key {
			return labels[i]
		}
	}
	return labels[0]
}

// trashFilterKeyOf is the stable key of an option label.
func trashFilterKeyOf(label string, labels, keys []string) (string, bool) {
	for i, l := range labels {
		if l == label {
			return keys[i], true
		}
	}
	return "", false
}

// filterTrash keeps the rows of the chosen type filter key.
func filterTrash(items []TrashItem, filterKey string) []TrashItem {
	if filterKey == "" || filterKey == trashFilterAll {
		return append([]TrashItem(nil), items...)
	}
	out := make([]TrashItem, 0, len(items))
	for _, item := range items {
		if item.Type == filterKey {
			out = append(out, item)
		}
	}
	return out
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
	case string(schema.EntityNote):
		return i18n.T("pages.trash.type_note")
	case string(schema.EntityPaper):
		return i18n.T("pages.trash.type_paper")
	case string(schema.EntityIdea):
		return i18n.T("pages.trash.type_idea")
	case string(schema.EntityExperiment):
		return i18n.T("pages.trash.type_experiment")
	case string(schema.EntityTask):
		return i18n.T("pages.trash.type_task")
	case string(schema.EntityRun):
		return i18n.T("pages.trash.type_run")
	}
	return t
}
