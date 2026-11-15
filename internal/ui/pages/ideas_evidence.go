package pages

// The evidence panel of the Idea detail page is SPEC-06 §6.5: the confirmed
// support and oppose columns, the unconfirmed AI drafts, the manual add form
// and the AI generation entry. Counts come from the Idea projection, so a
// draft never moves the support or oppose number (AC-18); a batch confirm or
// discard reports its partial failures per item instead of an all-or-nothing
// answer.

import (
	"context"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"researchos/internal/i18n"
	"researchos/internal/ideas"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// The narrow-window tabs of the evidence panel.
const (
	ideaEvidenceSupport = iota
	ideaEvidenceOppose
)

// ideaSourceKinds is the tab order of the evidence source picker (§6.5).
var ideaSourceKinds = []ideas.SourceType{
	ideas.SourcePaper,
	ideas.SourceNote,
	ideas.SourceExperiment,
	ideas.SourceRun,
}

// ideaSourceKindLabels are the i18n keys of ideaSourceKinds, in the same order.
var ideaSourceKindLabels = []string{
	"pages.ideas.source_paper",
	"pages.ideas.source_note",
	"pages.ideas.source_experiment",
	"pages.ideas.source_run",
}

// ideaEvidenceCard is one card's frame state: whether it is being edited, the
// Markdown draft, and the direction a draft may still change.
type ideaEvidenceCard struct {
	editing   bool
	text      string
	direction ideas.Direction
}

// evidenceTab renders the count bar, the drafts and the confirmed columns.
func (e *IdeaPage) evidenceTab(v *components.UI, a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	evidence := e.evidenceSnapshot()
	support := make([]ideas.EvidenceView, 0, len(evidence))
	oppose := make([]ideas.EvidenceView, 0, len(evidence))
	drafts := make([]ideas.EvidenceView, 0, len(evidence))
	for _, item := range evidence {
		switch {
		case item.AIGenerated && !item.Confirmed:
			drafts = append(drafts, item)
		case item.Direction == ideas.DirectionOppose:
			oppose = append(oppose, item)
		default:
			support = append(support, item)
		}
	}

	ui.Row(v.C).FillWidth().Gap(tokens.RelatedGap).AlignItems(ui.Center).Wrap().Children(func() {
		v.Badge(i18n.Tf("pages.ideas.evidence_support_n", map[string]string{
			"n": strconv.Itoa(detail.Evidence.Support),
		}), components.Success)
		v.Badge(i18n.Tf("pages.ideas.evidence_oppose_n", map[string]string{
			"n": strconv.Itoa(detail.Evidence.Oppose),
		}), components.Danger)
		if detail.Evidence.Drafts > 0 {
			v.Badge(i18n.Tf("pages.ideas.evidence_drafts_n", map[string]string{
				"n": strconv.Itoa(detail.Evidence.Drafts),
			}), components.Warning)
		}
		ui.Spacer(v.C)
		e.aiEntry(v, a, readOnly)
		v.Button(i18n.T("pages.ideas.btn_add_evidence"), components.ButtonOptions{
			Variant:  components.VariantSecondary,
			Icon:     icons.Plus,
			Disabled: readOnly,
			Tooltip:  readOnlyTooltip(readOnly),
			OnClick:  func() { e.resetAddForm(); e.addOpen = true },
		})
	})

	e.batchFailures(v)
	e.evidenceConfirmDialog(v, a, projectID)
	if len(drafts) > 0 {
		v.Card(components.CardOptions{Title: i18n.T("pages.ideas.drafts_card")}, func() {
			ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
				for _, item := range drafts {
					e.evidenceCard(v, a, projectID, item, readOnly)
				}
			})
		})
	}
	if len(evidence) == 0 {
		v.EmptyState(components.EmptyStateOptions{
			Icon:   icons.FileText,
			Title:  i18n.T("pages.ideas.evidence_empty_title"),
			Reason: i18n.T("pages.ideas.evidence_empty_reason"),
		})
		return
	}

	width, _ := v.C.Size()
	if width > 0 && width < ideasNarrowWidth {
		ui.Segmented(v.C, &e.evidenceSide,
			i18n.Tf("pages.ideas.evidence_support_n", map[string]string{"n": strconv.Itoa(len(support))}),
			i18n.Tf("pages.ideas.evidence_oppose_n", map[string]string{"n": strconv.Itoa(len(oppose))}),
		).Label(i18n.T("pages.ideas.evidence_side_label"))
		if e.evidenceSide == ideaEvidenceOppose {
			e.evidenceColumn(v, a, projectID, oppose, readOnly)
			return
		}
		e.evidenceColumn(v, a, projectID, support, readOnly)
		return
	}
	ui.Row(v.C).FillWidth().Gap(tokens.SectionGap).AlignItems(ui.Start).Children(func() {
		ui.Column(v.C).Grow(1).Gap(tokens.RelatedGap).Children(func() {
			ui.Text(v.C, i18n.Tf("pages.ideas.evidence_support_n", map[string]string{
				"n": strconv.Itoa(len(support)),
			})).FontSize(tokens.Typography.CardTitle.Size).
				FontWeight(int(tokens.WeightSemibold))
			e.evidenceColumn(v, a, projectID, support, readOnly)
		})
		ui.Column(v.C).Grow(1).Gap(tokens.RelatedGap).Children(func() {
			ui.Text(v.C, i18n.Tf("pages.ideas.evidence_oppose_n", map[string]string{
				"n": strconv.Itoa(len(oppose)),
			})).FontSize(tokens.Typography.CardTitle.Size).
				FontWeight(int(tokens.WeightSemibold))
			e.evidenceColumn(v, a, projectID, oppose, readOnly)
		})
	})
}

// evidenceColumn renders one direction's confirmed cards.
func (e *IdeaPage) evidenceColumn(v *components.UI, a *app.App, projectID string, items []ideas.EvidenceView, readOnly bool) {
	if len(items) == 0 {
		ui.Text(v.C, i18n.T("pages.ideas.evidence_none")).
			FontSize(tokens.Typography.Secondary.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted))
		return
	}
	ui.Column(v.C).FillWidth().Gap(tokens.RelatedGap).Children(func() {
		for _, item := range items {
			e.evidenceCard(v, a, projectID, item, readOnly)
		}
	})
}

// aiEntry is the 「用 AI 生成证据」 entry. Without a chat provider it is
// disabled and points at Settings (AC-21).
func (e *IdeaPage) aiEntry(v *components.UI, a *app.App, readOnly bool) {
	available := e.Services.Ideas.AIAvailable()
	v.Button(i18n.T("pages.ideas.btn_ai"), components.ButtonOptions{
		Variant:  components.VariantSecondary,
		Icon:     icons.Ideas,
		Disabled: readOnly || !available,
		Tooltip:  readOnlyTooltip(readOnly),
		OnClick:  func() { e.openAI() },
	})
	if !available {
		v.Button(i18n.T("pages.ideas.btn_settings"), components.ButtonOptions{
			Variant: components.VariantGhost,
			Size:    components.SizeSmall,
			Icon:    icons.Settings,
			Align:   ui.Center,
			OnClick: func() {
				a.Navigate(ports.Route{Kind: ports.RouteSettings, Params: map[string]string{"tab": "ai"}})
			},
		})
	}
}

// batchFailures reports the per-item failures of the last confirm or discard.
func (e *IdeaPage) batchFailures(v *components.UI) {
	e.mu.Lock()
	failures := append([]ideas.EvidenceFailure(nil), e.evidenceFailures...)
	e.mu.Unlock()
	if len(failures) == 0 {
		return
	}
	ui.Column(v.C).FillWidth().Gap(tokens.Space1).Children(func() {
		v.Banner(components.BannerOptions{
			Tone:    components.Warning,
			Title:   i18n.T("pages.ideas.batch_partial_title"),
			Message: i18n.T("pages.ideas.batch_partial_message"),
		})
		for _, failure := range failures {
			ui.Text(v.C, evidenceFailureText(failure)).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
}

// evidenceFailureText localises one refused batch item.
func evidenceFailureText(failure ideas.EvidenceFailure) string {
	short := failure.ID
	if len(short) > 8 {
		short = short[:8]
	}
	reason := ideaErrText(&ideas.ServiceError{Code: failure.Code, Message: failure.Message})
	return short + " · " + reason
}

// evidenceCard renders one evidence row. A draft has a dashed border, a 待确认
// badge and the accept, edit and discard actions; a confirmed card locks its
// direction (AC-17) and says that a content edit stays out of History.
func (e *IdeaPage) evidenceCard(v *components.UI, a *app.App, projectID string, item ideas.EvidenceView, readOnly bool) {
	p := v.T.Palette
	draft := item.AIGenerated && !item.Confirmed
	card := ui.Column(v.C).
		FillWidth().
		Padding(tokens.PanelPadding).
		Gap(tokens.ComponentGap).
		Background(mygo.Color(p.BgSurface)).
		Border(1, mygo.Color(p.BorderDefault)).
		Radius(tokens.RadiusLG)
	if draft {
		card.BorderStyle(ui.BorderDashed)
	}
	local := ui.Local(card, item.ID, func() ideaEvidenceCard {
		return ideaEvidenceCard{text: item.ContentMD, direction: item.Direction}
	})
	card.Children(func() {
		ui.Row(v.C).FillWidth().Gap(tokens.Space1).AlignItems(ui.Center).Wrap().Children(func() {
			v.Badge(ideaDirectionLabel(item.Direction), ideaDirectionTone(item.Direction))
			if item.AIGenerated {
				v.Badge(i18n.T("pages.ideas.ai_marker"), components.Accent)
			}
			if draft {
				v.Badge(i18n.T("pages.ideas.draft_marker"), components.Warning)
			}
			ui.Spacer(v.C)
			ui.Text(v.C, relativeTime(item.CreatedAt)).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.TextMuted))
		})

		if local.editing {
			e.evidenceEditor(v, a, projectID, item, local, draft)
		} else if strings.TrimSpace(item.ContentMD) == "" {
			ui.Text(v.C, i18n.T("pages.ideas.evidence_no_content")).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(p.TextMuted))
		} else {
			e.markdownView(v, item.ContentMD)
		}

		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Wrap().Children(func() {
			e.sourceButton(v, a, projectID, item)
			ui.Spacer(v.C)
			if local.editing {
				if v.Button(i18n.T("pages.ideas.evidence_save"), components.ButtonOptions{
					Variant: components.VariantPrimary,
					Size:    components.SizeSmall,
				}).Clicked() {
					e.saveEvidence(a, projectID, item, local, draft)
				}
				v.Button(i18n.T("common.cancel"), components.ButtonOptions{
					Size: components.SizeSmall,
					OnClick: func() {
						local.editing = false
						local.text = item.ContentMD
						local.direction = item.Direction
					},
				})
				return
			}
			if readOnly {
				return
			}
			if draft {
				if v.Button(i18n.T("pages.ideas.evidence_accept"), components.ButtonOptions{
					Variant: components.VariantPrimary,
					Size:    components.SizeSmall,
				}).Clicked() {
					e.openEvidenceConfirm(item.ID)
				}
				if v.Button(i18n.T("pages.ideas.evidence_discard"), components.ButtonOptions{
					Variant: components.VariantGhost,
					Size:    components.SizeSmall,
				}).Clicked() {
					e.discard(a, projectID, []string{item.ID})
				}
			}
			v.Button(i18n.T("pages.ideas.evidence_edit"), components.ButtonOptions{
				Variant: components.VariantGhost,
				Size:    components.SizeSmall,
				OnClick: func() { local.editing = true },
			})
			label := i18n.T("pages.ideas.evidence_delete")
			if draft {
				label = i18n.T("pages.ideas.evidence_discard")
			}
			v.Button(label, components.ButtonOptions{
				Variant: components.VariantGhost,
				Size:    components.SizeSmall,
				OnClick: func() {
					if draft {
						e.discard(a, projectID, []string{item.ID})
						return
					}
					e.deleteEvidence(a, projectID, item.ID)
				},
			})
		})
		if local.editing && !draft {
			ui.Text(v.C, i18n.T("pages.ideas.evidence_no_history")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.TextMuted))
		}
	})
}

// evidenceEditor is the small editor of one card. Only a draft may change its
// direction; a confirmed direction is locked (SPEC-06 §6.5).
func (e *IdeaPage) evidenceEditor(v *components.UI, a *app.App, projectID string, item ideas.EvidenceView, local *ideaEvidenceCard, draft bool) {
	v.Textarea(&local.text, components.FieldOptions{
		Label:     i18n.T("pages.ideas.field_evidence_content"),
		AutoFocus: true,
	})
	if draft {
		v.RadioGroup(&local.direction, []components.RadioOption[ideas.Direction]{
			{Value: ideas.DirectionSupport, Label: ideaDirectionLabel(ideas.DirectionSupport)},
			{Value: ideas.DirectionOppose, Label: ideaDirectionLabel(ideas.DirectionOppose)},
		}, components.RadioOptions{
			Label: i18n.T("pages.ideas.field_direction"),
			Row:   true,
		})
		return
	}
	ui.Text(v.C, i18n.T("pages.ideas.direction_locked")).
		FontSize(tokens.Typography.Caption.Size).
		TextColor(mygo.Color(v.T.Palette.TextMuted))
}

// sourceButton jumps to the evidence's source. A deleted source keeps the card
// and reads 「目标已删除」 without being jumpable (AC-19).
func (e *IdeaPage) sourceButton(v *components.UI, a *app.App, projectID string, item ideas.EvidenceView) {
	label := item.SourceTitle
	if strings.TrimSpace(label) == "" {
		label = ideaSourceTypeLabel(item.Source.Type) + " " + shortID(item.Source.ID)
	} else {
		label = ideaSourceTypeLabel(item.Source.Type) + " · " + label
	}
	missing := item.SourceDeleted || item.SourceBroken
	if missing {
		v.Button(i18n.T("pages.ideas.target_deleted"), components.ButtonOptions{
			Variant:  components.VariantGhost,
			Size:     components.SizeSmall,
			Disabled: true,
			Tooltip:  label,
		})
		return
	}
	v.Button(label, components.ButtonOptions{
		Variant: components.VariantGhost,
		Size:    components.SizeSmall,
		Icon:    icons.ExternalLink,
		OnClick: func() {
			e.jump(a, projectID, ideas.EntityRef{
				Type:      string(item.Source.Type),
				ID:        item.Source.ID,
				Title:     item.SourceTitle,
				Deleted:   item.SourceDeleted,
				BrokenRef: item.SourceBroken,
			})
		},
	})
}

// saveEvidence writes the card's content, and a draft's direction.
func (e *IdeaPage) saveEvidence(a *app.App, projectID string, item ideas.EvidenceView, local *ideaEvidenceCard, draft bool) {
	content := local.text
	if strings.TrimSpace(content) == "" {
		a.Toast(app.LevelWarning, i18n.T("pages.ideas.evidence_content_required"), "")
		return
	}
	in := ideas.UpdateEvidenceInput{
		EvidenceID: item.ID,
		ContentMD:  &content,
		Actor:      ports.ActorUser,
	}
	if draft && local.direction != item.Direction {
		direction := local.direction
		in.Direction = &direction
	}
	e.mu.Lock()
	if e.evidenceBusy {
		e.mu.Unlock()
		return
	}
	e.evidenceBusy = true
	e.mu.Unlock()

	api := e.Services.Ideas
	a.RunTask(i18n.T("pages.ideas.task_evidence_save"), func(ctx context.Context, t *app.Task) error {
		_, err := api.UpdateEvidence(ctx, projectID, in)
		e.mu.Lock()
		e.evidenceBusy = false
		if err != nil {
			e.action = ideaErrText(err)
		}
		e.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.ideas.toast_evidence_fail"), ideaErrText(err))
			a.Notify()
			return nil
		}
		local.editing = false
		e.shared.bump()
		a.Notify()
		return nil
	})
}

// openEvidenceConfirm gates accepting one AI draft behind an explicit
// confirmation; the dialog is the only path that calls confirm.
func (e *IdeaPage) openEvidenceConfirm(id string) {
	if id == "" {
		return
	}
	e.mu.Lock()
	busy := e.evidenceBusy
	e.mu.Unlock()
	if busy {
		return
	}
	e.evidenceConfirmID = id
	e.evidenceConfirmOpen = true
}

// evidenceConfirmDialog states that accepting a draft turns it into a research
// conclusion. Cancel leaves the draft untouched.
func (e *IdeaPage) evidenceConfirmDialog(v *components.UI, a *app.App, projectID string) {
	if !e.evidenceConfirmOpen {
		return
	}
	v.Dialog(&e.evidenceConfirmOpen, components.DialogOptions{
		Title:       i18n.T("pages.ideas.evidence_confirm_title"),
		Description: i18n.T("pages.ideas.evidence_confirm_desc"),
		Width:       tokens.Space12 * 8,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				e.evidenceConfirmOpen = false
				e.evidenceConfirmID = ""
			}
			if v.Button(i18n.T("pages.ideas.evidence_accept"), components.ButtonOptions{
				Variant: components.VariantPrimary,
			}).Clicked() {
				id := e.evidenceConfirmID
				e.evidenceConfirmOpen = false
				e.evidenceConfirmID = ""
				e.confirm(a, projectID, []string{id})
			}
		},
	}, func() {
		ui.Text(v.C, i18n.T("pages.ideas.evidence_confirm_hint")).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextSecondary))
	})
}

// confirm accepts drafits, reporting a partial batch honestly.
func (e *IdeaPage) confirm(a *app.App, projectID string, ids []string) {
	if len(ids) == 0 {
		return
	}
	e.mu.Lock()
	if e.evidenceBusy {
		e.mu.Unlock()
		return
	}
	e.evidenceBusy = true
	e.mu.Unlock()

	api := e.Services.Ideas
	ideaID := e.currentID()
	a.RunTask(i18n.T("pages.ideas.task_evidence_confirm"), func(ctx context.Context, t *app.Task) error {
		result, err := api.ConfirmEvidence(ctx, projectID, ideas.ConfirmEvidenceInput{
			IdeaID:      ideaID,
			EvidenceIDs: ids,
			Actor:       ports.ActorUser,
		})
		e.mu.Lock()
		e.evidenceBusy = false
		if err != nil {
			e.action = ideaErrText(err)
		} else {
			e.evidenceFailures = result.Failed
		}
		e.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.ideas.toast_evidence_fail"), ideaErrText(err))
			a.Notify()
			return nil
		}
		e.removeAIDrafts(result.Confirmed)
		e.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.ideas.toast_evidence_confirmed"), strconv.Itoa(len(result.Confirmed)))
		a.Notify()
		return nil
	})
}

// discard drops drafts, reporting a partial batch honestly.
func (e *IdeaPage) discard(a *app.App, projectID string, ids []string) {
	if len(ids) == 0 {
		return
	}
	e.mu.Lock()
	if e.evidenceBusy {
		e.mu.Unlock()
		return
	}
	e.evidenceBusy = true
	e.mu.Unlock()

	api := e.Services.Ideas
	ideaID := e.currentID()
	a.RunTask(i18n.T("pages.ideas.task_evidence_discard"), func(ctx context.Context, t *app.Task) error {
		result, err := api.DiscardEvidence(ctx, projectID, ideas.DiscardEvidenceInput{
			IdeaID:      ideaID,
			EvidenceIDs: ids,
			Actor:       ports.ActorUser,
		})
		e.mu.Lock()
		e.evidenceBusy = false
		if err != nil {
			e.action = ideaErrText(err)
		} else {
			e.evidenceFailures = result.Failed
		}
		e.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.ideas.toast_evidence_fail"), ideaErrText(err))
			a.Notify()
			return nil
		}
		e.removeAIDrafts(result.Discarded)
		e.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.ideas.toast_evidence_discarded"), strconv.Itoa(len(result.Discarded)))
		a.Notify()
		return nil
	})
}

// deleteEvidence soft-deletes one confirmed evidence row.
func (e *IdeaPage) deleteEvidence(a *app.App, projectID, evidenceID string) {
	e.mu.Lock()
	if e.evidenceBusy {
		e.mu.Unlock()
		return
	}
	e.evidenceBusy = true
	e.mu.Unlock()

	api := e.Services.Ideas
	a.RunTask(i18n.T("pages.ideas.task_evidence_delete"), func(ctx context.Context, t *app.Task) error {
		err := api.DeleteEvidence(ctx, projectID, evidenceID, ports.ActorUser)
		e.mu.Lock()
		e.evidenceBusy = false
		if err != nil {
			e.action = ideaErrText(err)
		}
		e.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.ideas.toast_evidence_fail"), ideaErrText(err))
			a.Notify()
			return nil
		}
		e.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.ideas.toast_evidence_deleted"), "")
		a.Notify()
		return nil
	})
}

// ---------------------------------------------------------------------------
// Manual add
// ---------------------------------------------------------------------------

// resetAddForm clears the manual add dialog.
func (e *IdeaPage) resetAddForm() {
	e.addDirection = ideas.DirectionSupport
	e.addContent = ""
	e.addSource = ideas.SourceRef{}
	e.addQuery = ""
	e.addErr = ""
	e.addBusy = false
}

// addEvidenceDialog is the manual form: direction, content and a mandatory
// source (§6.5). AddEvidence writes a confirmed row immediately (AC-14).
func (e *IdeaPage) addEvidenceDialog(v *components.UI, a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	if !e.addOpen || detail == nil {
		return
	}
	e.mu.Lock()
	busy, errText := e.addBusy, e.addErr
	e.mu.Unlock()
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title: i18n.T("pages.ideas.add_title"),
		Width: tokens.Space12 * 12,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				e.addOpen = false
			}
			valid := strings.TrimSpace(e.addContent) != "" && e.addSource.ID != ""
			if v.Button(i18n.T("pages.ideas.add_confirm"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  busy,
				Disabled: busy || readOnly || !valid,
			}).Clicked() && valid {
				e.addEvidence(a, projectID, detail, readOnly)
			}
		},
	}, func() {
		v.RadioGroup(&e.addDirection, []components.RadioOption[ideas.Direction]{
			{Value: ideas.DirectionSupport, Label: ideaDirectionLabel(ideas.DirectionSupport)},
			{Value: ideas.DirectionOppose, Label: ideaDirectionLabel(ideas.DirectionOppose)},
		}, components.RadioOptions{
			Label: i18n.T("pages.ideas.field_direction"),
			Row:   true,
		})
		v.Textarea(&e.addContent, components.FieldOptions{
			Label:       i18n.T("pages.ideas.field_evidence_content"),
			Placeholder: i18n.T("pages.ideas.field_evidence_hint"),
		})
		ui.Segmented(v.C, &e.addKind,
			i18n.T(ideaSourceKindLabels[0]),
			i18n.T(ideaSourceKindLabels[1]),
			i18n.T(ideaSourceKindLabels[2]),
			i18n.T(ideaSourceKindLabels[3]),
		).Label(i18n.T("pages.ideas.field_source"))
		v.SearchInput(&e.addQuery, components.SearchOptions{
			Label:       i18n.T("pages.ideas.link_search"),
			Placeholder: i18n.T("pages.ideas.link_search_hint"),
		})
		kind := ideaSourceKinds[e.addKind%len(ideaSourceKinds)]
		e.addCandidates.ensure(a, e.Services.Ideas, projectID, string(kind), e.addQuery)
		loading, items, candErr := e.addCandidates.snapshot()
		if candErr != "" {
			ui.Text(v.C, candErr).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
		if e.addSource.ID != "" {
			ui.Text(v.C, i18n.Tf("pages.ideas.source_chosen", map[string]string{
				"source": ideaSourceTypeLabel(e.addSource.Type) + " " + shortID(e.addSource.ID),
			})).FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextSecondary))
		}
		if loading && len(items) == 0 {
			v.Skeleton(components.SkeletonOptions{Lines: 2})
		}
		ui.Column(v.C).Gap(tokens.Space1).Children(func() {
			for _, ref := range items {
				if e.addSource.ID == ref.ID && string(kind) == ref.Type {
					continue
				}
				ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
					ui.Text(v.C, ref.Title).FontSize(tokens.Typography.Secondary.Size).Grow(1).SingleLine()
					v.Button(i18n.T("pages.ideas.source_pick"), components.ButtonOptions{
						Size: components.SizeSmall,
						OnClick: func() {
							e.addSource = ideas.SourceRef{Type: kind, ID: ref.ID}
						},
					})
				})
			}
		})
		if strings.TrimSpace(e.addContent) == "" || e.addSource.ID == "" {
			ui.Text(v.C, i18n.T("pages.ideas.source_required")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted))
		}
		if errText != "" {
			ui.Text(v.C, errText).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !busy {
		e.addOpen = false
	}
}

// addEvidence writes the manual row.
func (e *IdeaPage) addEvidence(a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	if readOnly {
		return
	}
	content := strings.TrimSpace(e.addContent)
	if content == "" || e.addSource.ID == "" {
		e.addErr = i18n.T("pages.ideas.source_required")
		return
	}
	e.mu.Lock()
	if e.addBusy {
		e.mu.Unlock()
		return
	}
	e.addBusy = true
	e.addErr = ""
	e.mu.Unlock()

	api := e.Services.Ideas
	ideaID := detail.ID
	direction := e.addDirection
	source := e.addSource
	a.RunTask(i18n.T("pages.ideas.task_evidence_add"), func(ctx context.Context, t *app.Task) error {
		_, err := api.AddEvidence(ctx, projectID, ideas.AddEvidenceInput{
			IdeaID:    ideaID,
			Direction: direction,
			ContentMD: content,
			Source:    source,
			Actor:     ports.ActorUser,
		})
		e.mu.Lock()
		e.addBusy = false
		if err != nil {
			e.addErr = ideaErrText(err)
		}
		e.mu.Unlock()
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.ideas.toast_evidence_fail"), ideaErrText(err))
			a.Notify()
			return nil
		}
		e.addOpen = false
		e.resetAddForm()
		e.shared.bump()
		a.Toast(app.LevelSuccess, i18n.T("pages.ideas.toast_evidence_added"), "")
		a.Notify()
		return nil
	})
}

// ---------------------------------------------------------------------------
// Shared rendering helpers
// ---------------------------------------------------------------------------

// markdownView renders evidence content with the same parser and formula hooks
// as the description preview.
func (e *IdeaPage) markdownView(v *components.UI, text string) {
	if e.md == nil {
		e.prepare()
	}
	markdown.View(v.C, text, e.md).FillWidth()
}

// ideaDirectionLabel is the localised direction name.
func ideaDirectionLabel(direction ideas.Direction) string {
	if direction == ideas.DirectionOppose {
		return i18n.T("pages.ideas.direction_oppose")
	}
	return i18n.T("pages.ideas.direction_support")
}

// ideaDirectionTone is the badge tone of a direction.
func ideaDirectionTone(direction ideas.Direction) components.Tone {
	if direction == ideas.DirectionOppose {
		return components.Danger
	}
	return components.Success
}

// ideaSourceTypeLabel is the localised name of a source type.
func ideaSourceTypeLabel(typ ideas.SourceType) string {
	for i, known := range ideaSourceKinds {
		if known == typ {
			return i18n.T(ideaSourceKindLabels[i])
		}
	}
	return string(typ)
}

// shortID shortens an identifier for a card's source line.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
