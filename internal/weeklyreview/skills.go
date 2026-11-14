package weeklyreview

// A skill is the user-editable writing instruction set behind report
// generation. The hard contract of SPEC-09 §5.6 (seven sections, refs, no
// invented numbers) is code and cannot be edited; a skill only shapes how the
// model writes inside it: what to emphasise, how to judge an idea's support,
// what counts as progress. The builtin skill is always available and cannot be
// removed or overwritten.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"researchos/internal/foundation/settings"
)

// Skill is one writing instruction set.
type Skill struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
	// Builtin marks the shipped skill: read-only, always present.
	Builtin bool `json:"builtin,omitempty"`
}

// SkillSet is the workspace's skills plus the selected default.
type SkillSet struct {
	Skills  []Skill `json:"skills"`
	Default string  `json:"default"`
}

// SkillInput creates or updates one skill; an empty ID creates.
type SkillInput struct {
	ID     string
	Name   string
	Prompt string
}

// The skill settings keys and limits.
const (
	settingSkills       = "weekly.skills"
	settingDefaultSkill = "weekly.skill_default"

	builtinSkillID     = "default"
	maxSkills          = 20
	maxSkillNameRunes  = 60
	maxSkillPromptSize = 8000
)

// builtinSkill is the shipped default: the house style the prompt contract
// expects, and nothing that depends on a user's field.
var builtinSkill = Skill{
	ID:      builtinSkillID,
	Name:    "默认科研写作规范",
	Builtin: true,
	Prompt: `- 读者是导师和合作者：三分钟读完就知道这一周推进了什么、卡在哪里。
- 先给结论再给过程：写「完成了什么 / 现在什么状态」，不按时间复述操作。
- 同一实体或同一主题的多条记录合并成一条，不重复同一件事。
- 写价值：为什么这一步对本课题有意义；没有证据的价值判断标「（待确认）」。
- Idea 要写清它的判断是什么、当前证据偏向哪边（支持 / 反驳 / 不足）、下一步验证怎么做。
- 实验与运行只写结果与状态，不写「成功 / 失败」结论。
- 下周计划写清依据（周计划 / 截止日 / 顺延），不把已完成的写成计划。`,
}

// Skills returns the builtin skill followed by the stored ones.
func (s *service) Skills(ctx context.Context) (SkillSet, error) {
	return s.loadSkillSet(ctx)
}

// SaveSkill creates or updates one user skill and returns the new set.
func (s *service) SaveSkill(ctx context.Context, in SkillInput) (SkillSet, error) {
	name := strings.TrimSpace(in.Name)
	prompt := strings.TrimSpace(in.Prompt)
	if name == "" {
		return SkillSet{}, serviceError(ErrInvalidInput, "Skill 需要一个名称")
	}
	if len([]rune(name)) > maxSkillNameRunes {
		return SkillSet{}, serviceError(ErrInvalidInput, "Skill 名称超过 %d 字", maxSkillNameRunes)
	}
	if prompt == "" {
		return SkillSet{}, serviceError(ErrInvalidInput, "Skill 内容不能为空")
	}
	if len(prompt) > maxSkillPromptSize {
		return SkillSet{}, serviceError(ErrInvalidInput, "Skill 内容超过 %d KiB", maxSkillPromptSize>>10)
	}
	store, err := s.skillStore()
	if err != nil {
		return SkillSet{}, err
	}
	set, err := s.loadSkillSet(ctx)
	if err != nil {
		return SkillSet{}, err
	}
	stored := storedSkills(set)
	if in.ID == "" || in.ID == builtinSkillID {
		if in.ID == builtinSkillID {
			return SkillSet{}, serviceError(ErrInvalidInput, "内置 Skill 不可修改；请复制成新 Skill 再编辑")
		}
		if len(stored) >= maxSkills {
			return SkillSet{}, serviceError(ErrInvalidInput, "最多保留 %d 个 Skill", maxSkills)
		}
		id, ierr := s.newID()
		if ierr != nil {
			return SkillSet{}, ierr
		}
		stored = append(stored, Skill{ID: id, Name: name, Prompt: prompt})
	} else {
		found := false
		for i := range stored {
			if stored[i].ID == in.ID {
				stored[i].Name, stored[i].Prompt = name, prompt
				found = true
				break
			}
		}
		if !found {
			return SkillSet{}, serviceError(ErrNotFound, "Skill 不存在")
		}
	}
	if err := s.writeSkills(ctx, store, stored); err != nil {
		return SkillSet{}, err
	}
	return s.loadSkillSet(ctx)
}

// DeleteSkill removes one user skill. Deleting the current default falls back
// to the builtin.
func (s *service) DeleteSkill(ctx context.Context, id string) (SkillSet, error) {
	if id == builtinSkillID {
		return SkillSet{}, serviceError(ErrInvalidInput, "内置 Skill 不可删除")
	}
	store, err := s.skillStore()
	if err != nil {
		return SkillSet{}, err
	}
	set, err := s.loadSkillSet(ctx)
	if err != nil {
		return SkillSet{}, err
	}
	stored := storedSkills(set)
	kept := stored[:0]
	found := false
	for _, skill := range stored {
		if skill.ID == id {
			found = true
			continue
		}
		kept = append(kept, skill)
	}
	if !found {
		return SkillSet{}, serviceError(ErrNotFound, "Skill 不存在")
	}
	if err := s.writeSkills(ctx, store, kept); err != nil {
		return SkillSet{}, err
	}
	if set.Default == id {
		if err := s.writeDefaultSkill(ctx, store, ""); err != nil {
			return SkillSet{}, err
		}
	}
	return s.loadSkillSet(ctx)
}

// SetDefaultSkill selects the default skill; empty or builtin means the
// shipped one.
func (s *service) SetDefaultSkill(ctx context.Context, id string) (SkillSet, error) {
	store, err := s.skillStore()
	if err != nil {
		return SkillSet{}, err
	}
	if id != "" && id != builtinSkillID {
		set, lerr := s.loadSkillSet(ctx)
		if lerr != nil {
			return SkillSet{}, lerr
		}
		found := false
		for _, skill := range storedSkills(set) {
			if skill.ID == id {
				found = true
				break
			}
		}
		if !found {
			return SkillSet{}, serviceError(ErrNotFound, "Skill 不存在")
		}
	}
	if id == builtinSkillID {
		id = ""
	}
	if err := s.writeDefaultSkill(ctx, store, id); err != nil {
		return SkillSet{}, err
	}
	return s.loadSkillSet(ctx)
}

// resolveSkill returns the skill one generation runs with. An unknown explicit
// id is an input error; an unknown stored default falls back to the builtin.
func (s *service) resolveSkill(ctx context.Context, id string) (Skill, error) {
	set, err := s.loadSkillSet(ctx)
	if err != nil {
		return Skill{}, err
	}
	want := id
	if want == "" {
		want = set.Default
	}
	if want == "" || want == builtinSkillID {
		return builtinSkill, nil
	}
	for _, skill := range set.Skills {
		if skill.ID == want {
			return skill, nil
		}
	}
	return Skill{}, serviceError(ErrInvalidInput, "Skill 不存在：%s", want)
}

// loadSkillSet reads the stored skills and the default selection. A workspace
// without a settings store still has the builtin skill.
func (s *service) loadSkillSet(ctx context.Context) (SkillSet, error) {
	set := SkillSet{Skills: []Skill{builtinSkill}}
	store, err := s.skillStore()
	if err != nil {
		// No workspace settings: the builtin skill is all there is.
		return set, nil
	}
	raw, ok, err := store.Get(ctx, settingSkills)
	if err != nil {
		return set, fmt.Errorf("weeklyreview: read skills: %w", err)
	}
	if ok && strings.TrimSpace(raw) != "" {
		var stored []Skill
		if err := json.Unmarshal([]byte(raw), &stored); err != nil {
			return set, serviceError(ErrInvalidInput, "Skill 配置损坏，请重新保存")
		}
		for _, skill := range stored {
			if skill.ID == "" || skill.ID == builtinSkillID || strings.TrimSpace(skill.Name) == "" {
				continue
			}
			set.Skills = append(set.Skills, skill)
		}
	}
	if def, ok, err := store.Get(ctx, settingDefaultSkill); err == nil && ok {
		for _, skill := range set.Skills {
			if skill.ID == def {
				set.Default = skill.ID
				break
			}
		}
	}
	return set, nil
}

// storedSkills drops the builtin from a set.
func storedSkills(set SkillSet) []Skill {
	out := make([]Skill, 0, len(set.Skills))
	for _, skill := range set.Skills {
		if !skill.Builtin && skill.ID != builtinSkillID {
			out = append(out, Skill{ID: skill.ID, Name: skill.Name, Prompt: skill.Prompt})
		}
	}
	return out
}

// skillStore resolves the workspace settings store that holds the skills.
func (s *service) skillStore() (*settings.Store, error) {
	if s.settings != nil {
		return s.settings, nil
	}
	if s.ws == nil || s.ws.Global() == nil {
		return nil, errors.New("weeklyreview: no workspace settings for skills")
	}
	return settings.NewWorkspace(s.ws.Global().DB()), nil
}

func (s *service) writeSkills(ctx context.Context, store *settings.Store, skills []Skill) error {
	raw, err := json.Marshal(skills)
	if err != nil {
		return fmt.Errorf("weeklyreview: encode skills: %w", err)
	}
	if err := store.Set(ctx, nil, settingSkills, string(raw)); err != nil {
		return fmt.Errorf("weeklyreview: save skills: %w", err)
	}
	return nil
}

func (s *service) writeDefaultSkill(ctx context.Context, store *settings.Store, id string) error {
	if err := store.Set(ctx, nil, settingDefaultSkill, id); err != nil {
		return fmt.Errorf("weeklyreview: save default skill: %w", err)
	}
	return nil
}
