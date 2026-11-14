package pages

// The writing-skill editor is shared by the AI settings tab and the weekly
// generation dialog: a skill is a named instruction set that shapes how the
// model writes a report section, while the validation contract stays code.
// Both surfaces read and write the same workspace-level set.

import (
	"context"
	"strings"

	"github.com/egoist/mygo/ui"

	"researchos/internal/i18n"
	"researchos/internal/ui/app"
	"researchos/internal/ui/components"
	"researchos/internal/ui/icons"
	"researchos/internal/ui/mygo"
	"researchos/internal/ui/tokens"
	"researchos/internal/weeklyreview"
)

// weeklySkillEditor is the dialog state of one skill edit or copy.
type weeklySkillEditor struct {
	open bool
	// id is the skill being updated; empty creates a new one.
	id     string
	name   string
	prompt string
	busy   bool
	err    string
}

// newSkill starts from a base skill's text (the builtin, or any user skill) as
// a new skill.
func (e *weeklySkillEditor) newSkill(base weeklyreview.Skill) {
	e.open = true
	e.id = ""
	e.name = base.Name
	e.prompt = base.Prompt
	e.err = ""
}

// editSkill edits an existing user skill in place.
func (e *weeklySkillEditor) editSkill(skill weeklyreview.Skill) {
	e.open = true
	e.id = skill.ID
	e.name = skill.Name
	e.prompt = skill.Prompt
	e.err = ""
}

// dialog renders the editor. onSaved receives the set the service returned.
func (e *weeklySkillEditor) dialog(v *components.UI, a *app.App, svc *Services, projectID string, onSaved func(weeklyreview.SkillSet)) {
	if !e.open {
		return
	}
	title := i18n.T("pages.weekly_review.skill_new_title")
	if e.id != "" {
		title = i18n.T("pages.weekly_review.skill_edit_title")
	}
	open := true
	v.Dialog(&open, components.DialogOptions{
		Title:       title,
		Description: i18n.T("pages.weekly_review.skill_edit_desc"),
		Width:       640,
		Actions: func() {
			if v.Button(i18n.T("common.cancel"), components.ButtonOptions{}).Clicked() {
				e.open = false
			}
			if v.Button(i18n.T("common.save"), components.ButtonOptions{
				Variant:  components.VariantPrimary,
				Loading:  e.busy,
				Disabled: e.busy,
			}).Clicked() {
				e.save(a, svc, projectID, onSaved)
			}
		},
	}, func() {
		v.Input(&e.name, components.FieldOptions{
			Label:       i18n.T("pages.weekly_review.skill_name"),
			Placeholder: i18n.T("pages.weekly_review.skill_name_hint"),
			Disabled:    e.busy,
		})
		area := v.Textarea(&e.prompt, components.FieldOptions{
			Label:       i18n.T("pages.weekly_review.skill_prompt"),
			Description: i18n.T("pages.weekly_review.skill_prompt_hint"),
			Disabled:    e.busy,
		})
		area.MinHeight(tokens.Space12 * 7)
		if e.err != "" {
			ui.Text(v.C, e.err).
				FontSize(tokens.Typography.Secondary.Size).
				TextColor(mygo.Color(v.T.Palette.StatusDanger))
		}
	})
	if !open && !e.busy {
		e.open = false
	}
}

func (e *weeklySkillEditor) save(a *app.App, svc *Services, projectID string, onSaved func(weeklyreview.SkillSet)) {
	if e.busy || a == nil || svc == nil || svc.Weekly == nil {
		e.err = i18n.T("pages.weekly_review.disabled_title")
		return
	}
	api := svc.Weekly
	in := weeklyreview.SkillInput{ID: e.id, Name: e.name, Prompt: e.prompt}
	e.busy = true
	e.err = ""
	a.RunTask(i18n.T("pages.weekly_review.skill_task_save"), func(ctx context.Context, t *app.Task) error {
		set, err := api.SaveSkill(ctx, projectID, in)
		e.busy = false
		if err != nil {
			e.err = weeklyError(err)
			a.Notify()
			return nil
		}
		e.open = false
		if onSaved != nil {
			onSaved(set)
		}
		a.Toast(app.LevelSuccess, i18n.T("pages.weekly_review.skill_saved"), "")
		a.Notify()
		return nil
	})
}

// weeklySkillCard lists the skills with their actions: copy, make default,
// edit and delete.
func weeklySkillCard(v *components.UI, a *app.App, svc *Services, projectID string, set weeklyreview.SkillSet, editor *weeklySkillEditor, onChanged func(weeklyreview.SkillSet)) {
	v.Card(components.CardOptions{
		Title: i18n.T("pages.settings.ai.card_skills"),
		Actions: func() {
			if v.Button(i18n.T("pages.weekly_review.skill_new"), components.ButtonOptions{
				Variant: components.VariantPrimary,
				Size:    components.SizeSmall,
				Icon:    icons.Plus,
			}).Clicked() {
				editor.newSkill(weeklySkillBuiltin(set))
			}
		},
	}, func() {
		ui.Text(v.C, i18n.T("pages.weekly_review.skill_card_hint")).
			FontSize(tokens.Typography.Caption.Size).
			TextColor(mygo.Color(v.T.Palette.TextMuted))
		for _, skill := range set.Skills {
			skill := skill
			isDefault := skill.ID == set.Default || (set.Default == "" && skill.Builtin)
			ui.Column(v.C).FillWidth().Padding(tokens.Space2, tokens.Space3).
				Gap(tokens.Space2).
				Background(mygo.Color(v.T.Palette.BgSurface)).
				Border(1, mygo.Color(v.T.Palette.BorderDefault)).
				Radius(tokens.RadiusLG).
				Children(func() {
					ui.Row(v.C).FillWidth().Gap(tokens.Space2).AlignItems(ui.Center).Children(func() {
						ui.Text(v.C, skill.Name).FontWeight(int(tokens.WeightSemibold))
						if skill.Builtin {
							v.Badge(i18n.T("pages.weekly_review.skill_builtin"), components.Neutral)
						}
						if isDefault {
							v.Badge(i18n.T("pages.weekly_review.skill_default_badge"), components.Success)
						}
						ui.Spacer(v.C)
						if !skill.Builtin {
							if v.Button(i18n.T("pages.weekly_review.skill_edit"), components.ButtonOptions{
								Variant: components.VariantSecondary, Size: components.SizeSmall,
							}).Clicked() {
								editor.editSkill(skill)
							}
						}
						if v.Button(i18n.T("pages.weekly_review.skill_copy"), components.ButtonOptions{
							Variant: components.VariantGhost, Size: components.SizeSmall,
						}).Clicked() {
							editor.newSkill(skill)
						}
						if !skill.Builtin && !isDefault {
							if v.Button(i18n.T("pages.weekly_review.skill_make_default"), components.ButtonOptions{
								Variant: components.VariantGhost, Size: components.SizeSmall,
							}).Clicked() {
								weeklySkillSetDefault(a, svc, projectID, skill.ID, onChanged)
							}
						}
						if !skill.Builtin {
							v.IconButton(icons.Trash, i18n.T("pages.weekly_review.skill_delete"), components.IconButtonOptions{
								Variant: components.VariantGhost,
								Size:    components.SizeSmall,
								OnClick: func() {
									weeklySkillDelete(a, svc, projectID, skill.ID, onChanged)
								},
							})
						}
					})
					ui.Text(v.C, weeklySkillPreview(skill.Prompt)).
						FontSize(tokens.Typography.Caption.Size).
						TextColor(mygo.Color(v.T.Palette.TextMuted)).
						MaxLines(2)
				})
		}
	})
}

// weeklySkillPicker is the generation dialog's writing-skill choice plus the
// entry to edit it: the same skills the AI settings tab manages. label is the
// select's text, id the resolved skill.
func weeklySkillPicker(v *components.UI, a *app.App, svc *Services, projectID string, set weeklyreview.SkillSet, label, id *string, editor *weeklySkillEditor) {
	if len(set.Skills) == 0 {
		return
	}
	labels := make([]string, 0, len(set.Skills))
	ids := make([]string, 0, len(set.Skills))
	for _, skill := range set.Skills {
		text := skill.Name
		if skill.ID == set.Default || (set.Default == "" && skill.Builtin) {
			text += " " + i18n.T("pages.weekly_review.skill_default_suffix")
		}
		labels = append(labels, text)
		ids = append(ids, skill.ID)
	}
	if *label == "" || indexOf(labels, *label) < 0 {
		if index := indexOf(ids, set.Default); index >= 0 {
			*label, *id = labels[index], ids[index]
		} else {
			*label, *id = labels[0], ids[0]
		}
	} else {
		*id = ids[indexOf(labels, *label)]
	}
	ui.Row(v.C).FillWidth().Gap(tokens.ComponentGap).AlignItems(ui.End).Wrap().Children(func() {
		sel := v.Select(label, components.SelectOptions{
			Label:   i18n.T("pages.weekly_review.field_skill"),
			Options: labels,
		})
		if sel.Changed() {
			if index := indexOf(labels, *label); index >= 0 {
				*id = ids[index]
			}
		}
		if v.Button(i18n.T("pages.weekly_review.skill_edit"), components.ButtonOptions{
			Variant: components.VariantGhost,
			Size:    components.SizeSmall,
			Icon:    icons.Edit,
		}).Clicked() {
			for _, skill := range set.Skills {
				if skill.ID != *id {
					continue
				}
				if skill.Builtin {
					editor.newSkill(skill)
				} else {
					editor.editSkill(skill)
				}
				break
			}
		}
	})
}

// weeklySkillBuiltin returns the shipped skill, the base of a new one.
func weeklySkillBuiltin(set weeklyreview.SkillSet) weeklyreview.Skill {
	for _, skill := range set.Skills {
		if skill.Builtin {
			return skill
		}
	}
	return weeklyreview.Skill{}
}

// weeklySkillPreview is the first line of a skill's guidance.
func weeklySkillPreview(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if text != "" {
			return text
		}
	}
	return ""
}

// weeklySkillSetDefault promotes one skill to the workspace default.
func weeklySkillSetDefault(a *app.App, svc *Services, projectID, id string, onChanged func(weeklyreview.SkillSet)) {
	if a == nil || svc == nil || svc.Weekly == nil {
		return
	}
	api := svc.Weekly
	a.RunTask(i18n.T("pages.weekly_review.skill_task_default"), func(ctx context.Context, t *app.Task) error {
		set, err := api.SetDefaultSkill(ctx, projectID, id)
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.weekly_review.skill_default_failed"), weeklyError(err))
			a.Notify()
			return nil
		}
		if onChanged != nil {
			onChanged(set)
		}
		a.Notify()
		return nil
	})
}

// weeklySkillDelete removes one user skill.
func weeklySkillDelete(a *app.App, svc *Services, projectID, id string, onChanged func(weeklyreview.SkillSet)) {
	if a == nil || svc == nil || svc.Weekly == nil {
		return
	}
	api := svc.Weekly
	a.RunTask(i18n.T("pages.weekly_review.skill_task_delete"), func(ctx context.Context, t *app.Task) error {
		set, err := api.DeleteSkill(ctx, projectID, id)
		if err != nil {
			a.Toast(app.LevelDanger, i18n.T("pages.weekly_review.skill_delete_failed"), weeklyError(err))
			a.Notify()
			return nil
		}
		if onChanged != nil {
			onChanged(set)
		}
		a.Toast(app.LevelSuccess, i18n.T("pages.weekly_review.skill_deleted"), "")
		a.Notify()
		return nil
	})
}
