package pages

// The history tab of the Idea detail page is SPEC-06 §6.7: a self-drawn
// vertical timeline, newest first, with kind filters, actor badges and jumpable
// references. There is no framework timeline, so the dots and the connecting
// rule are drawn from tokens; the list is read-only (AC-30).

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"researchos/internal/i18n"
	"researchos/internal/ideas"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// ideaHistoryPageSize is one page of the timeline; the cursor walks the whole
// history (AC-28).
const ideaHistoryPageSize = 50

// ideaHistoryKinds is the chip order of the kind filter.
var ideaHistoryKinds = []ideas.HistoryKind{
	ideas.HistoryCreated,
	ideas.HistoryStatus,
	ideas.HistoryRelation,
	ideas.HistoryEvidence,
	ideas.HistoryAI,
	ideas.HistoryDerived,
	ideas.HistoryDeleted,
	ideas.HistoryRestored,
}

// ideaHistoryKindLabels are the i18n keys of ideaHistoryKinds, in order.
var ideaHistoryKindLabels = []string{
	"pages.ideas.history_kind_created",
	"pages.ideas.history_kind_status",
	"pages.ideas.history_kind_relation",
	"pages.ideas.history_kind_evidence",
	"pages.ideas.history_kind_ai",
	"pages.ideas.history_kind_derived",
	"pages.ideas.history_kind_deleted",
	"pages.ideas.history_kind_restored",
}

// ideaHistory caches one filtered page chain of the timeline. The frame reads
// the cache; the task fills it.
type ideaHistory struct {
	mu      sync.Mutex
	loading bool
	loaded  bool
	ideaID  string
	key     string
	version int
	entries []ideas.HistoryEntry
	cursor  string
	errText string
}

// ensure reloads the timeline when the Idea, the filter or the shared
// generation changed.
func (h *ideaHistory) ensure(a *app.App, api IdeasAPI, projectID, ideaID string, kinds []ideas.HistoryKind, version int) {
	key := historyKindKey(kinds)
	h.mu.Lock()
	need := !h.loading && (h.ideaID != ideaID || h.key != key || !h.loaded || h.version != version)
	h.mu.Unlock()
	if need {
		h.fetch(a, api, projectID, ideaID, kinds, "", version)
	}
}

// loadMore appends the next page.
func (h *ideaHistory) loadMore(a *app.App, api IdeasAPI, projectID, ideaID string, kinds []ideas.HistoryKind, version int) {
	h.mu.Lock()
	cursor := h.cursor
	h.mu.Unlock()
	if cursor == "" {
		return
	}
	h.fetch(a, api, projectID, ideaID, kinds, cursor, version)
}

// fetch reads one page from the cursor.
func (h *ideaHistory) fetch(a *app.App, api IdeasAPI, projectID, ideaID string, kinds []ideas.HistoryKind, cursor string, version int) {
	h.mu.Lock()
	if h.loading {
		h.mu.Unlock()
		return
	}
	h.loading = true
	key := historyKindKey(kinds)
	h.ideaID = ideaID
	h.key = key
	h.mu.Unlock()

	queryKinds := append([]ideas.HistoryKind(nil), kinds...)
	a.RunTask(i18n.T("pages.ideas.task_history"), func(ctx context.Context, t *app.Task) error {
		page, err := api.History(ctx, projectID, ideas.HistoryQuery{
			IdeaID:   ideaID,
			Kinds:    queryKinds,
			PageSize: ideaHistoryPageSize,
			Cursor:   cursor,
		})
		h.mu.Lock()
		h.loading = false
		if err != nil {
			h.errText = ideaErrText(err)
		} else {
			if cursor == "" {
				h.entries = page.Items
			} else {
				h.entries = append(h.entries, page.Items...)
			}
			h.cursor = page.NextCursor
			h.loaded = true
			h.version = version
			h.errText = ""
		}
		h.mu.Unlock()
		a.Notify()
		return nil
	})
}

// snapshot returns the cached timeline.
func (h *ideaHistory) snapshot() ([]ideas.HistoryEntry, string, string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]ideas.HistoryEntry(nil), h.entries...), h.cursor, h.errText, h.loading
}

// historyKindKey is the canonical identity of a kind filter set.
func historyKindKey(kinds []ideas.HistoryKind) string {
	if len(kinds) == 0 {
		return ""
	}
	parts := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		parts = append(parts, string(kind))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// historyTab renders the filter chips, the timeline and the load-more action.
func (e *IdeaPage) historyTab(v *components.UI, a *app.App, projectID string, detail *ideas.IdeaDetail) {
	e.historyChips(v)
	e.history.ensure(a, e.Services.Ideas, projectID, detail.ID, e.historyKinds, e.shared.version())
	entries, cursor, errText, loading := e.history.snapshot()

	if errText != "" {
		v.Banner(components.BannerOptions{
			Tone:    components.Danger,
			Title:   i18n.T("pages.ideas.history_error"),
			Message: errText,
		})
	}
	if len(entries) == 0 {
		if loading {
			v.Skeleton(components.SkeletonOptions{Lines: 5})
			return
		}
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.Clock,
			Title:  i18n.T("pages.ideas.history_empty_title"),
			Reason: i18n.T("pages.ideas.history_empty_reason"),
		})
		return
	}
	ui.Column(v.C).FillWidth().Gap(tokens.Space1).Children(func() {
		for i, entry := range entries {
			e.historyRow(v, a, projectID, entry, i == len(entries)-1)
		}
	})
	if cursor != "" {
		v.Button(i18n.T("pages.ideas.history_more"), components.ButtonOptions{
			Variant:  components.VariantSecondary,
			Loading:  loading,
			Disabled: loading,
			OnClick: func() {
				e.history.loadMore(a, e.Services.Ideas, projectID, detail.ID, e.historyKinds, e.shared.version())
			},
		})
	}
}

// historyChips toggles the kind filter, with 全部 first.
func (e *IdeaPage) historyChips(v *components.UI) {
	ui.Row(v.C).FillWidth().Gap(tokens.Space1).Wrap().AlignItems(ui.Center).Children(func() {
		e.historyChip(v, i18n.T("pages.ideas.chip_all"), len(e.historyKinds) == 0, func() {
			e.historyKinds = nil
		})
		for i, kind := range ideaHistoryKinds {
			kind := kind
			label := i18n.T(ideaHistoryKindLabels[i])
			on := hasHistoryKind(e.historyKinds, kind)
			e.historyChip(v, label, on, func() {
				e.historyKinds = toggleHistoryKind(e.historyKinds, kind)
			})
		}
	})
}

func (e *IdeaPage) historyChip(v *components.UI, label string, selected bool, onClick func()) {
	options := components.ButtonOptions{
		Variant: components.VariantSecondary,
		Size:    components.SizeSmall,
		OnClick: onClick,
	}
	if selected {
		options.Variant = components.VariantPrimary
		options.Icon = icons.Check
	}
	v.Button(label, options)
}

func hasHistoryKind(kinds []ideas.HistoryKind, kind ideas.HistoryKind) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

func toggleHistoryKind(kinds []ideas.HistoryKind, kind ideas.HistoryKind) []ideas.HistoryKind {
	out := make([]ideas.HistoryKind, 0, len(kinds)+1)
	found := false
	for _, k := range kinds {
		if k == kind {
			found = true
			continue
		}
		out = append(out, k)
	}
	if !found {
		out = append(out, kind)
	}
	return out
}

// historyRow is one timeline entry: a dot, the connecting rule, the summary,
// the actor badge, the transition and the jumpable reference.
func (e *IdeaPage) historyRow(v *components.UI, a *app.App, projectID string, entry ideas.HistoryEntry, last bool) {
	p := v.T.Palette
	dot := mygo.Color(ideaHistoryColor(v, entry))
	ui.Row(v.C).FillWidth().Gap(tokens.RelatedGap).AlignItems(ui.Start).Children(func() {
		ui.Column(v.C).Width(tokens.Space3).AlignItems(ui.Center).Children(func() {
			ui.Box(v.C).Size(tokens.Space2, tokens.Space2).Radius(tokens.Space1).Background(dot)
			if !last {
				ui.Box(v.C).Width(1).MinHeight(tokens.Space6).Background(mygo.Color(p.BorderDefault))
			}
		})
		ui.Column(v.C).Grow(1).Gap(tokens.Space1).Padding(0, 0, tokens.RelatedGap, 0).Children(func() {
			ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Wrap().Children(func() {
				ui.Text(v.C, entry.Summary).
					FontSize(tokens.Typography.Body.Size).
					TextColor(mygo.Color(p.TextPrimary)).
					Grow(1).
					MaxLines(3)
				v.Badge(ideaActorLabel(entry.Actor), ideaActorTone(entry.Actor))
				if entry.Synthesized {
					v.Badge(i18n.T("pages.ideas.history_synthesized"), components.Info)
				}
			})
			ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Wrap().Children(func() {
				ui.Text(v.C, ideaTimestamp(entry.At)).
					FontSize(tokens.Typography.Caption.Size).
					FontFeatures("tnum").
					TextColor(mygo.Color(p.TextMuted))
				if transition := historyTransition(entry); transition != "" {
					ui.Text(v.C, transition).
						FontSize(tokens.Typography.Caption.Size).
						TextColor(mygo.Color(p.TextSecondary))
				}
				if entry.Ref != nil {
					e.historyRef(v, a, projectID, *entry.Ref)
				}
			})
		})
	})
}

// historyRef is the jumpable object of an entry. A deleted or broken target
// reads 目标已删除 and stays put.
func (e *IdeaPage) historyRef(v *components.UI, a *app.App, projectID string, ref ideas.EntityRef) {
	label := ref.Title
	if strings.TrimSpace(label) == "" {
		label = shortID(ref.ID)
	}
	if ref.Deleted || ref.BrokenRef {
		ui.Text(v.C, label+" · "+i18n.T("pages.ideas.target_deleted")).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted))
		return
	}
	v.Button(label, components.ButtonOptions{
		Variant: components.VariantGhost,
		Size:    components.SizeSmall,
		Icon:    icons.ExternalLink,
		OnClick: func() { e.jump(a, projectID, ref) },
	})
}

// historyTransition renders the old and new value of a status entry with
// localised status names.
func historyTransition(entry ideas.HistoryEntry) string {
	if entry.Old == "" || entry.New == "" {
		return ""
	}
	old, few := ideas.Status(entry.Old), ideas.Status(entry.New)
	if old.Valid() && few.Valid() {
		return ideaStatusLabel(old) + " → " + ideaStatusLabel(few)
	}
	return entry.Old + " → " + entry.New
}

// ideaHistoryColor is the dot colour of an entry kind.
func ideaHistoryColor(v *components.UI, entry ideas.HistoryEntry) tokens.Color {
	p := v.T.Palette
	switch entry.Kind {
	case ideas.HistoryCreated:
		return p.AccentDefault
	case ideas.HistoryStatus:
		return p.StatusInfo
	case ideas.HistoryRelation:
		return p.StatusSuccess
	case ideas.HistoryEvidence:
		return p.StatusSuccess
	case ideas.HistoryAI:
		return p.AccentDefault
	case ideas.HistoryDerived:
		return p.StatusInfo
	case ideas.HistoryDeleted:
		return p.StatusDanger
	case ideas.HistoryRestored:
		return p.StatusSuccess
	}
	return p.BorderStrong
}

// ideaActorLabel localises a History actor.
func ideaActorLabel(actor ideas.Actor) string {
	switch string(actor) {
	case "mcp":
		return i18n.T("pages.ideas.actor_mcp")
	case "ai":
		return i18n.T("pages.ideas.actor_ai")
	}
	return i18n.T("pages.ideas.actor_user")
}

// ideaActorTone gives the three actors distinct badge tones.
func ideaActorTone(actor ideas.Actor) components.Tone {
	switch string(actor) {
	case "mcp":
		return components.Neutral
	case "ai":
		return components.Accent
	}
	return components.Info
}
