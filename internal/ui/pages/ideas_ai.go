package pages

// The AI draft review of the evidence panel is SPEC-06 §6.6: choose the
// sources the skill should read, run evidence.normalize through a managed task
// with progress and cancel, then review every draft — accept one, edit then
// accept, or discard. Only unedited drafts can be accepted in a batch, so
// model text never becomes confirmed without being looked at (AC-22/AC-27).

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"researchos/internal/i18n"
	"researchos/internal/ideas"
	"researchos/internal/ports"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
)

// ideaAIGroups are the source groups of the picker, with the candidates cache
// index each one uses.
var ideaAIGroups = []struct {
	typ   string
	label string
}{
	{string(ideas.SourceExperiment), "pages.ideas.ai_group_experiments"},
	{string(ideas.SourceRun), "pages.ideas.ai_group_runs"},
	{string(ideas.SourceNote), "pages.ideas.ai_group_notes"},
}

// ideaDraft is one generated draft under review. Content is the editable
// buffer; the draft counts as edited once it differs from the stored text.
type ideaDraft struct {
	Evidence ideas.Evidence
	Content  string
	Busy     bool
	Err      string
}

// edited reports whether the reviewer changed the model's text.
func (d *ideaDraft) edited() bool { return d.Content != d.Evidence.ContentMD }

// openAI opens the review dialog and clears the previous failure.
func (e *IdeaPage) openAI() {
	e.mu.Lock()
	e.aiOpen = true
	e.aiErrCode = ""
	e.aiErrText = ""
	e.mu.Unlock()
}

// aiDialog is the whole review flow in one dialog: sources, instruction, the
// generate action and the drafts it produced.
func (e *IdeaPage) aiDialog(v *components.UI, a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	e.mu.Lock()
	open := e.aiOpen
	busy := e.aiBusy
	errCode, errText := e.aiErrCode, e.aiErrText
	drafts := append([]*ideaDraft(nil), e.aiDrafts...)
	skipped := append([]ideas.SkippedDraft(nil), e.aiSkipped...)
	provider, model := e.aiProvider, e.aiModel
	task := e.aiTask
	e.mu.Unlock()
	if !open || detail == nil {
		return
	}
	openPtr := true
	v.Dialog(&openPtr, components.DialogOptions{
		Title:       i18n.T("pages.ideas.ai_title"),
		Description: i18n.T("pages.ideas.ai_desc"),
		Width:       tokens.Space12 * 14,
		Actions: func() {
			if busy && task != nil {
				if v.Button(i18n.T("pages.ideas.ai_cancel"), components.ButtonOptions{
					Variant: components.VariantDanger,
					Size:    components.SizeSmall,
				}).Clicked() {
					task.Cancel()
				}
			}
			if v.Button(i18n.T("pages.ideas.ai_close"), components.ButtonOptions{}).Clicked() {
				e.mu.Lock()
				e.aiOpen = false
				e.mu.Unlock()
			}
			if v.Button(i18n.T("pages.ideas.ai_generate"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  busy,
				Disabled: busy || readOnly || len(e.aiOrder) == 0,
			}).Clicked() {
				e.generateAI(a, projectID, detail, readOnly)
			}
		},
	}, func() {
		e.aiSources(v, a, projectID, readOnly)
		field := v.Input(&e.aiInstruction, components.FieldOptions{
			Label:       i18n.T("pages.ideas.ai_instruction"),
			Placeholder: i18n.T("pages.ideas.ai_instruction_hint"),
			Error:       aiInstructionError(e.aiInstruction),
			Disabled:    readOnly,
		})
		if field.Submitted() {
			e.generateAI(a, projectID, detail, readOnly)
		}
		if busy {
			ui.Row(v.C).Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
				v.Spinner(icons.SizeDefault)
				ui.Text(v.C, i18n.T("pages.ideas.ai_running")).
					FontSize(tokens.Typography.Secondary.Size).
					TextColor(mygo.Color(v.T.Palette.TextSecondary))
			})
		}
		if errText != "" {
			ui.Text(v.C, i18n.Tf("pages.ideas.ai_failed", map[string]string{
				"code": errCode,
				"text": errText,
			})).FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
			v.Button(i18n.T("pages.ideas.ai_retry"), components.ButtonOptions{
				Variant:  components.VariantSecondary,
				Size:     components.SizeSmall,
				Icon:     icons.Refresh,
				Disabled: busy || readOnly,
				OnClick:  func() { e.generateAI(a, projectID, detail, readOnly) },
			})
		}
		if provider != "" || model != "" {
			ui.Text(v.C, i18n.Tf("pages.ideas.ai_run_provenance", map[string]string{
				"provider": provider,
				"model":    model,
			})).FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(v.T.Palette.TextMuted))
		}
		if !busy && len(drafts) == 0 && errText == "" && e.aiRan() {
			ui.Text(v.C, i18n.T("pages.ideas.ai_empty")).
				FontSize(tokens.Typography.Body.Size).
				TextColor(mygo.Color(v.T.Palette.TextPrimary))
		}
		if len(skipped) > 0 {
			ui.Column(v.C).Gap(tokens.Space1).Children(func() {
				ui.Text(v.C, i18n.T("pages.ideas.ai_skipped")).
					FontSize(tokens.Typography.Caption.Size).
					FontWeight(int(tokens.WeightSemibold)).
					TextColor(mygo.Color(v.T.Palette.TextSecondary))
				for _, item := range skipped {
					ui.Text(v.C, string(item.Source.Type)+" "+shortID(item.Source.ID)+" · "+item.Reason).
						FontSize(tokens.Typography.Caption.Size).
						TextColor(mygo.Color(v.T.Palette.TextMuted))
				}
			})
		}
		if len(drafts) > 0 {
			unedited := e.uneditedDraftIDs(drafts)
			v.Button(i18n.Tf("pages.ideas.ai_accept_all", map[string]string{
				"n": strconv.Itoa(len(unedited)),
			}), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Size:     components.SizeSmall,
				Disabled: len(unedited) == 0 || readOnly,
				OnClick:  func() { e.acceptDrafts(a, projectID, unedited, drafts) },
			})
			ui.Column(v.C).Gap(tokens.RelatedGap).Children(func() {
				for _, draft := range drafts {
					e.draftCard(v, a, projectID, draft, readOnly)
				}
			})
		}
	})
	if !openPtr && !busy {
		e.mu.Lock()
		e.aiOpen = false
		e.mu.Unlock()
	}
}

// aiRan reports whether a generation finished, so an empty draft list is the
// explicit 「未发现可规范化证据」 answer instead of an empty dialog.
func (e *IdeaPage) aiRan() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.aiFinished
}

// aiSources is the grouped multi-select of experiments, runs and notes, with
// the resolved text length of every chosen source (§6.6).
func (e *IdeaPage) aiSources(v *components.UI, a *app.App, projectID string, readOnly bool) {
	ui.Column(v.C).FillWidth().Gap(tokens.RelatedGap).Children(func() {
		for i, group := range ideaAIGroups {
			if i >= len(e.aiGroups) {
				break
			}
			e.aiGroups[i].ensure(a, e.Services.Ideas, projectID, group.typ, "")
			loading, items, errText := e.aiGroups[i].snapshot()
			ui.Column(v.C).Gap(tokens.Space1).Children(func() {
				ui.Text(v.C, i18n.T(group.label)).
					FontSize(tokens.Typography.Caption.Size).
					FontWeight(int(tokens.WeightSemibold)).
					TextColor(mygo.Color(v.T.Palette.TextSecondary))
				if errText != "" {
					ui.Text(v.C, errText).
						FontSize(tokens.Typography.Caption.Size).
						TextColor(mygo.Color(v.T.Palette.StatusDanger))
					return
				}
				if loading && len(items) == 0 {
					v.Skeleton(components.SkeletonOptions{Lines: 2})
					return
				}
				if len(items) == 0 {
					ui.Text(v.C, i18n.T("pages.ideas.ai_group_empty")).
						FontSize(tokens.Typography.Caption.Size).
						TextColor(mygo.Color(v.T.Palette.TextMuted))
					return
				}
				for _, ref := range items {
					source := ideas.SourceRef{Type: ideas.SourceType(group.typ), ID: ref.ID}
					key := sourceKey(source)
					checked := e.sourceSelected(source)
					// The check box is used directly rather than through the field wrapper:
					// Changed is reported on the widget itself.
					cb := ui.Checkbox(v.C, &checked, aiSourceLabel(v, source, ref.Title, e.aiLens[key])).
						Disabled(readOnly)
					if cb.Changed() {
						e.toggleSource(a, projectID, source, checked)
					}
				}
			})
		}
	})
}

// aiSourceLabel is the checkbox label of one source: its title and, once
// resolved, how much text it carries.
func aiSourceLabel(v *components.UI, source ideas.SourceRef, title, length string) string {
	label := title
	if label == "" {
		label = shortID(source.ID)
	}
	if length != "" {
		label += " · " + length
	}
	return label
}

// toggleSource updates the selection and starts the length preview of a
// source that was just chosen.
func (e *IdeaPage) toggleSource(a *app.App, projectID string, source ideas.SourceRef, selected bool) {
	key := sourceKey(source)
	e.mu.Lock()
	if selected {
		if _, ok := e.aiSelected[key]; !ok {
			e.aiSelected[key] = source
			e.aiOrder = append(e.aiOrder, key)
		}
	} else {
		delete(e.aiSelected, key)
		kept := e.aiOrder[:0]
		for _, k := range e.aiOrder {
			if k != key {
				kept = append(kept, k)
			}
		}
		e.aiOrder = kept
	}
	e.mu.Unlock()
	if selected {
		e.resolveLength(a, projectID, source)
	}
}

// resolveLength fills the preview of one source through the resolver.
func (e *IdeaPage) resolveLength(a *app.App, projectID string, source ideas.SourceRef) {
	key := sourceKey(source)
	e.mu.Lock()
	if e.aiResolving[key] {
		e.mu.Unlock()
		return
	}
	e.aiResolving[key] = true
	e.mu.Unlock()

	api := e.Services.Ideas
	a.RunTask(i18n.T("pages.ideas.task_source_preview"), func(ctx context.Context, t *app.Task) error {
		resolved, err := api.ResolveSource(ctx, projectID, source)
		e.mu.Lock()
		delete(e.aiResolving, key)
		switch {
		case err != nil:
			e.aiLens[key] = ideaErrText(err)
		case resolved.Deleted:
			e.aiLens[key] = i18n.T("pages.ideas.ai_source_deleted")
		case strings.TrimSpace(resolved.Text) == "":
			e.aiLens[key] = i18n.T("pages.ideas.ai_source_empty")
		default:
			text := i18n.Tf("pages.ideas.ai_source_len", map[string]string{
				"n": strconv.Itoa(utf8.RuneCountInString(resolved.Text)),
			})
			if resolved.Truncated {
				text += " · " + i18n.T("pages.ideas.ai_source_truncated")
			}
			e.aiLens[key] = text
		}
		e.mu.Unlock()
		a.Notify()
		return nil
	})
}

// sourceSelected reports whether a source is in the selection.
func (e *IdeaPage) sourceSelected(source ideas.SourceRef) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.aiSelected[sourceKey(source)]
	return ok
}

// sourceKey is the identity of a source in the selection maps.
func sourceKey(source ideas.SourceRef) string {
	return string(source.Type) + "/" + source.ID
}

// aiInstructionError validates the optional instruction before a run.
func aiInstructionError(text string) string {
	if utf8.RuneCountInString(text) > ideasMaxInstructionRunes {
		return i18n.Tf("pages.ideas.ai_instruction_long", map[string]string{
			"max": strconv.Itoa(ideasMaxInstructionRunes),
		})
	}
	return ""
}

// generateAI runs evidence.normalize in a managed task. The selection survives
// a failure, so a retry costs one click (AC-25).
func (e *IdeaPage) generateAI(a *app.App, projectID string, detail *ideas.IdeaDetail, readOnly bool) {
	if readOnly {
		return
	}
	if errText := aiInstructionError(e.aiInstruction); errText != "" {
		a.Toast(app.LevelWarning, i18n.T("pages.ideas.ai_instruction"), errText)
		return
	}
	e.mu.Lock()
	if len(e.aiOrder) == 0 {
		e.mu.Unlock()
		a.Toast(app.LevelWarning, i18n.T("pages.ideas.ai_no_sources"), "")
		return
	}
	if e.aiBusy {
		e.mu.Unlock()
		return
	}
	sources := make([]ideas.SourceRef, 0, len(e.aiOrder))
	for _, key := range e.aiOrder {
		if source, ok := e.aiSelected[key]; ok {
			sources = append(sources, source)
		}
	}
	instruction := e.aiInstruction
	e.aiBusy = true
	e.aiErrCode = ""
	e.aiErrText = ""
	e.aiDrafts = nil
	e.aiSkipped = nil
	e.aiProvider = ""
	e.aiModel = ""
	e.aiFinished = false
	e.mu.Unlock()

	api := e.Services.Ideas
	ideaID := detail.ID
	task := a.RunTask(i18n.T("pages.ideas.task_ai"), func(ctx context.Context, t *app.Task) error {
		t.Stage(i18n.T("pages.ideas.ai_stage_read"))
		result, err := api.GenerateEvidenceDrafts(ctx, projectID, ideas.GenerateEvidenceInput{
			IdeaID:      ideaID,
			Sources:     sources,
			Instruction: strings.TrimSpace(instruction),
			Actor:       ports.ActorUser,
		})
		canceled := ctx.Err() != nil
		e.mu.Lock()
		e.aiBusy = false
		e.aiTask = nil
		e.aiFinished = true
		switch {
		case canceled:
			e.aiErrCode = "canceled"
			e.aiErrText = i18n.T("pages.ideas.ai_canceled")
		case err != nil:
			e.aiErrCode = ideaErrCode(err)
			e.aiErrText = ideaErrText(err)
		default:
			drafts := make([]*ideaDraft, 0, len(result.Drafts))
			for _, evidence := range result.Drafts {
				drafts = append(drafts, &ideaDraft{Evidence: evidence, Content: evidence.ContentMD})
			}
			e.aiDrafts = drafts
			e.aiSkipped = result.Skipped
			e.aiProvider = result.Provider
			e.aiModel = result.Model
		}
		e.mu.Unlock()
		if err == nil && !canceled {
			e.shared.bump()
		}
		a.Notify()
		return nil
	})
	e.mu.Lock()
	e.aiTask = task
	e.mu.Unlock()
}

// draftCard is one generated draft under review: direction, editable content,
// the source, the collapsible rationale and the provenance.
func (e *IdeaPage) draftCard(v *components.UI, a *app.App, projectID string, draft *ideaDraft, readOnly bool) {
	p := v.T.Palette
	card := ui.Column(v.C).
		FillWidth().
		Padding(tokens.PanelPadding).
		Gap(tokens.ComponentGap).
		Background(mygo.Color(p.BgSurface)).
		Border(1, mygo.Color(p.BorderDefault)).
		BorderStyle(ui.BorderDashed).
		Radius(tokens.RadiusLG)
	rationaleOpen := ui.Local(card, "rationale", func() bool { return false })
	card.Children(func() {
		ui.Row(v.C).FillWidth().Gap(tokens.Space1).AlignItems(ui.Center).Wrap().Children(func() {
			v.Badge(ideaDirectionLabel(draft.Evidence.Direction), ideaDirectionTone(draft.Evidence.Direction))
			v.Badge(i18n.T("pages.ideas.ai_marker"), components.Accent)
			ui.Spacer(v.C)
			ui.Text(v.C, string(draft.Evidence.Source.Type)+" "+shortID(draft.Evidence.Source.ID)).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.TextMuted))
		})
		v.Textarea(&draft.Content, components.FieldOptions{
			Label:    i18n.T("pages.ideas.field_evidence_content"),
			Disabled: readOnly,
		})
		if draft.edited() {
			ui.Text(v.C, i18n.T("pages.ideas.ai_edited")).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.StatusWarning))
		}
		e.provenance(v, draft, rationaleOpen)
		ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.Center).Children(func() {
			ui.Spacer(v.C)
			v.Button(i18n.T("pages.ideas.ai_accept"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Size:     components.SizeSmall,
				Loading:  draft.Busy,
				Disabled: draft.Busy || readOnly,
				OnClick:  func() { e.acceptDrafts(a, projectID, []string{draft.Evidence.ID}, []*ideaDraft{draft}) },
			})
			v.Button(i18n.T("pages.ideas.ai_discard"), components.ButtonOptions{
				Variant:  components.VariantGhost,
				Size:     components.SizeSmall,
				Disabled: draft.Busy || readOnly,
				OnClick:  func() { e.discardDraft(a, projectID, draft) },
			})
		})
		if draft.Err != "" {
			ui.Text(v.C, draft.Err).
				FontSize(tokens.Typography.Caption.Size).
				TextColor(mygo.Color(p.StatusDanger))
		}
	})
}

// provenance renders the model identity and the collapsible rationale.
func (e *IdeaPage) provenance(v *components.UI, draft *ideaDraft, rationaleOpen *bool) {
	prov := draft.Evidence.Provenance
	if prov == nil {
		return
	}
	ui.Text(v.C, i18n.Tf("pages.ideas.ai_provenance", map[string]string{
		"provider": prov.ProviderID,
		"model":    prov.ModelID,
		"version":  prov.PromptVersion,
		"time":     ideaTimestamp(prov.GeneratedAt),
	})).FontSize(tokens.Typography.Caption.Size).
		TextColor(mygo.Color(v.T.Palette.TextMuted))
	if strings.TrimSpace(prov.Rationale) == "" {
		return
	}
	ui.Collapsible(v.C, i18n.T("pages.ideas.ai_rationale"), rationaleOpen, func() {
		ui.Text(v.C, prov.Rationale).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextSecondary)).
			MaxLines(12)
	})
}

// uneditedDraftIDs are the drafts a batch may accept: the ones the reviewer
// has not changed, so unviewed model text never flips silently (§6.6).
func (e *IdeaPage) uneditedDraftIDs(drafts []*ideaDraft) []string {
	out := make([]string, 0, len(drafts))
	for _, draft := range drafts {
		if draft.edited() || draft.Busy {
			continue
		}
		out = append(out, draft.Evidence.ID)
	}
	return out
}

// acceptDrafts saves an edited content first, then confirms the ids. One
// refused item does not abort the rest (batch semantics of the service).
func (e *IdeaPage) acceptDrafts(a *app.App, projectID string, ids []string, drafts []*ideaDraft) {
	if len(ids) == 0 {
		return
	}
	api := e.Services.Ideas
	ideaID := e.currentID()
	contents := map[string]string{}
	busy := map[*ideaDraft]bool{}
	e.mu.Lock()
	for _, draft := range drafts {
		if draft.edited() && containsID(ids, draft.Evidence.ID) {
			contents[draft.Evidence.ID] = draft.Content
		}
		draft.Busy = true
		busy[draft] = true
	}
	e.mu.Unlock()

	a.RunTask(i18n.T("pages.ideas.task_ai_accept"), func(ctx context.Context, t *app.Task) error {
		confirmed := make([]string, 0, len(ids))
		failures := make([]ideas.EvidenceFailure, 0)
		for _, id := range ids {
			if content, ok := contents[id]; ok {
				if _, err := api.UpdateEvidence(ctx, projectID, ideas.UpdateEvidenceInput{
					EvidenceID: id,
					ContentMD:  &content,
					Actor:      ports.ActorUser,
				}); err != nil {
					failures = append(failures, ideas.EvidenceFailure{ID: id, Code: ideas.CodeOf(err), Message: err.Error()})
					continue
				}
			}
			result, err := api.ConfirmEvidence(ctx, projectID, ideas.ConfirmEvidenceInput{
				IdeaID:      ideaID,
				EvidenceIDs: []string{id},
				Actor:       ports.ActorUser,
			})
			if err != nil {
				failures = append(failures, ideas.EvidenceFailure{ID: id, Code: ideas.CodeOf(err), Message: err.Error()})
				continue
			}
			confirmed = append(confirmed, result.Confirmed...)
			failures = append(failures, result.Failed...)
		}
		e.mu.Lock()
		for draft := range busy {
			draft.Busy = false
			if containsID(failuresIDs(failures), draft.Evidence.ID) {
				draft.Err = evidenceFailureText(ideas.EvidenceFailure{
					ID:   draft.Evidence.ID,
					Code: failureCode(failures, draft.Evidence.ID),
				})
			}
		}
		e.evidenceFailures = failures
		if len(confirmed) > 0 {
			e.aiDrafts = removeDrafts(e.aiDrafts, confirmed)
		}
		e.mu.Unlock()
		if len(confirmed) > 0 {
			e.shared.bump()
			a.Toast(app.LevelSuccess, i18n.T("pages.ideas.toast_evidence_confirmed"), strconv.Itoa(len(confirmed)))
		}
		a.Notify()
		return nil
	})
}

// failureCode returns the code of one failed batch item.
func failureCode(failures []ideas.EvidenceFailure, id string) ideas.ErrorCode {
	for _, failure := range failures {
		if failure.ID == id {
			return failure.Code
		}
	}
	return ideas.ErrAIOutputInvalid
}

// failuresIDs lists the ids of a failure slice.
func failuresIDs(failures []ideas.EvidenceFailure) []string {
	out := make([]string, 0, len(failures))
	for _, failure := range failures {
		out = append(out, failure.ID)
	}
	return out
}

// discardDraft drops one reviewed draft.
func (e *IdeaPage) discardDraft(a *app.App, projectID string, draft *ideaDraft) {
	api := e.Services.Ideas
	ideaID := e.currentID()
	e.mu.Lock()
	draft.Busy = true
	e.mu.Unlock()
	a.RunTask(i18n.T("pages.ideas.task_ai_discard"), func(ctx context.Context, t *app.Task) error {
		result, err := api.DiscardEvidence(ctx, projectID, ideas.DiscardEvidenceInput{
			IdeaID:      ideaID,
			EvidenceIDs: []string{draft.Evidence.ID},
			Actor:       ports.ActorUser,
		})
		e.mu.Lock()
		draft.Busy = false
		if err != nil {
			draft.Err = ideaErrText(err)
		} else if len(result.Failed) > 0 {
			draft.Err = evidenceFailureText(result.Failed[0])
		} else {
			e.aiDrafts = removeDrafts(e.aiDrafts, result.Discarded)
		}
		e.mu.Unlock()
		if len(result.Discarded) > 0 {
			a.Toast(app.LevelSuccess, i18n.T("pages.ideas.toast_evidence_discarded"), "")
		}
		a.Notify()
		return nil
	})
}

// removeAIDrafts forgets the reviewed drafts a confirm or discard removed.
func (e *IdeaPage) removeAIDrafts(ids []string) {
	e.mu.Lock()
	e.aiDrafts = removeDrafts(e.aiDrafts, ids)
	e.mu.Unlock()
}

// removeDrafts returns drafts without the removed ids.
func removeDrafts(drafts []*ideaDraft, ids []string) []*ideaDraft {
	if len(drafts) == 0 || len(ids) == 0 {
		return drafts
	}
	out := make([]*ideaDraft, 0, len(drafts))
	for _, draft := range drafts {
		if containsID(ids, draft.Evidence.ID) {
			continue
		}
		out = append(out, draft)
	}
	return out
}

// containsID reports whether id is in ids.
func containsID(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
