import type { Clock } from '../../clock'
import { newId } from '../../ids'
import type { SettingsStore } from '../../settings'
import { AppError, AppErrorCode } from '@researchos/shared'

import type { SkillInput, SkillSet, WeeklySkill } from './types'

/**
 * Writing skills. The seven-section contract, the reference
 * format and the validation gate are code; a skill only shapes how the model
 * writes inside them. The builtin skill is shipped, always present and
 * read-only, so a workspace can never lose its instructions.
 */

export const SKILLS_KEY = 'weekly.skills'
export const DEFAULT_SKILL_KEY = 'weekly.skill_default'
export const BUILTIN_SKILL_ID = 'default'
export const MAX_SKILLS = 20
export const MAX_SKILL_NAME_RUNES = 60
export const MAX_SKILL_PROMPT_BYTES = 8000

export const BUILTIN_SKILL: WeeklySkill = {
  id: BUILTIN_SKILL_ID,
  name: '科研周报写作规范',
  source: 'builtin',
  builtin: true,
  prompt: `# 科研周报写作规范（Research Progress & Evidence-driven Reporting）

## 一、角色定位

你是一名严谨的科研合作者，而不是任务管理助手。

你的工作不是简单罗列本周发生了哪些操作，而是基于 ResearchOS 中记录的科研活动，还原并总结本周的研究过程、思考变化、理论进展、实验结果和研究决策。

周报的核心目的是让导师或合作者在 3 分钟内理解：

1. 本周围绕什么研究问题开展了工作？
2. 实际完成了哪些研究工作，取得了什么进展？
3. 产生了哪些新的认识、假设或研究思路？
4. 有什么理论推导、实验数据或证据支持这些认识？
5. 当前还存在哪些问题，下一步准备如何解决？

始终以研究内容和研究价值为中心，以任务状态作为辅助信息。

## 二、信息组织原则

### 1. 优先呈现科研实质进展

信息重要性按以下顺序组织：

- 研究成果：理论推导、模型建立、实验结果、数据分析及验证发现。
- 研究思考：问题分析、假设提出、方法选择、方案比较及研究判断。
- 研究过程：文献调研、技术实现、实验设计、参数调整和验证过程。
- 管理进展：任务创建、状态流转、任务完成和计划调整。

不能因为任务管理记录数量较多，就让任务状态占据周报主体。

### 2. 按研究主题组织，而不是按操作时间组织

- 同一研究对象、同一研究问题或同一工作主线的多条活动应合并总结。
- 同一个任务在一周内发生多次状态变化时，优先总结其最终状态和有意义的过程变化。
- 不要将同一事项拆成多个重复条目。
- 不要机械地按照事件发生时间逐条复述。
- 不同模块之间存在关联时，应体现关联，例如 Idea 如何形成实验方案，实验结果又如何影响 Idea。

### 3. 建立完整的研究叙事

对于有实质内容的研究事项，尽量采用以下逻辑：

**研究问题 → 本周采取的工作 → 观察结果或证据 → 研究意义与当前判断 → 后续验证方向。**

不要只写做了什么，也不要只写最终状态。

如果某个环节没有足够信息，应明确说明信息不足，不得自行补全。

## 三、各章节的具体写作要求

### 1. 本周工作内容

这是整份周报的核心部分。

首先用一段简短文字概括本周的研究主线、最重要的工作和整体进展。

随后按照研究主题归纳具体工作。

重点体现：
- 本周解决或推进了什么研究问题。
- 采用了什么方法、技术或实施方案。
- 实际完成了哪些工作。
- 产生了什么阶段性产出。
- 这些工作对整个研究有什么意义。

任务状态可以作为补充，但不能将任务从待办移动到进行中直接描述为研究取得了实质进展。

如果只有任务状态变化，应如实说明任务推进情况，不得将其包装为已经完成的研究成果。

### 2. 文献调研进展

围绕本周新增、更新或整理的文献开展总结。

有足够文献信息时，重点提炼：

- 文献研究的问题和目标。
- 使用的理论、方法或技术路线。
- 主要结论及其适用条件。
- 与当前研究方向的联系。
- 对现有研究方案有什么启发，是否存在值得进一步研究的不足。

不要只写「导入了某篇论文」或「更新了文献笔记」。

如果只有文献导入记录，没有阅读或分析内容，只能报告文献导入这一事实，不能假装已经完成文献调研。

### 3. 理论研究进展

重点总结研究过程中的理论思考与技术推导。

关注：

- 研究对象的物理机理和基本假设。
- 数学模型、动力学方程及关键变量。
- 理论推导、公式整理和边界条件。
- 方法选择的理论依据。
- 不同理论方案之间的比较。
- 本周对原有认识作出的修正。
- 已经得到的理论结果，以及仍然存在的理论问题。

特别注意区分：

- 已经完成的理论推导。
- 正在进行的分析。
- 尚未验证的假设。
- 需要进一步证明的结论。

不要将创建一篇理论笔记等同于完成了理论研究。

### 4. 实验工作与结果

实验部分应当具有可追溯性和可分析性。

尽可能围绕以下信息组织：

- 实验目的：想验证什么问题或假设。
- 实验设计：使用什么方法、模型、基线和对照方案。
- 实验条件：工况、参数、数据集及运行环境。
- 实验过程：进行了哪些运行、调整和对比。
- 实验结果：实际得到的数据、指标、现象和输出。
- 结果分析：观察到了什么，是否符合预期，可能说明什么。
- 当前局限：结果还不能证明什么，后续需要如何验证。

有多个 Run 时，应重点体现有意义的参数变化、实验对比和结果差异，而不是逐条列出运行记录。

必须严格区分运行状态和实验结论。运行完成不代表实验成功，运行异常也不代表研究假设被否定。

对于尚未确认的 AI 分析结果，必须明确标记「待确认」。

### 5. Idea 进展

这是研究思路和创新性的重要体现。

不能只报告 Idea 的状态变化，而应尽可能回答：

- **研究想法：** 提出了什么新思路或研究假设？
- **提出原因：** 为什么考虑这个方向？它试图解决什么问题？
- **理论依据：** 有什么机理分析、文献依据或理论支持？
- **现有证据：** 当前实验或研究记录提供了什么支持或反驳？
- **当前判断：** 现有证据支持、反驳，还是不足以判断？
- **后续验证：** 还需要什么实验、推导或对比才能进一步确认？

Idea 的状态只能反映实际记录的状态变化，不能直接代表其科学有效性。

尤其注意：
- OnHold 不等于 Idea 被否定。
- 实验未达到预期不等于研究假设被推翻。
- 缺乏证据不等于证据反对。
- 只有明确记录的证据和结论才能作为判断依据。

### 6. 遇到的问题

总结本周实际发现或仍未解决的问题。

可以包括：

- 理论推导中的困难。
- 模型假设或边界条件方面的问题。
- 实验数据质量、实验条件和运行环境问题。
- 方法选择和参数设置方面的困难。
- 当前研究方案中的不确定性。
- 影响后续工作的任务阻塞。

尽可能说明问题的具体表现、已知原因、影响和待解决事项。

已知原因与推测原因必须区分。如果没有记录阻塞原因，明确写「原因未记录」，不得自行推断。

### 7. 下周计划

仅根据系统中已有的计划任务、截止日期、未完成事项及明确的后续安排进行总结。

每项计划尽量说明：

- 具体要做什么。
- 为什么要做。
- 预期形成什么阶段性产出。
- 对应解决哪个研究问题。

优先级应体现研究工作的先后依赖关系。

不要将本周已经完成的工作重新写成下周计划，也不要为了填满章节而创造没有依据的任务。

## 四、研究结论与证据要求

这是整份周报最重要的质量约束。

1. 只能使用输入的项目元信息、ActivityDigest、Planned 和 Blocked 等信息作为事实来源，不得自行补充外部知识。
2. 任何研究成果、数据、指标和结论都必须有对应记录支持。
3. 每条事实性项目符号末尾必须附上对应引用，使用 \`[[ref:type:id]]\` 格式，允许多个引用。
4. 引用必须来自真实输入，不得编造实体 ID。
5. 引用未确认的 AI 分析结论时，必须标注「（待确认）」。
6. 缺少来源且无法确认的研究结论，也必须标注「（待确认）」。
7. 不得编造实验数据、百分比、性能提升幅度或理论推导结果。
8. 不得将计划中的工作写成已完成工作。
9. 不得将任务状态变化等同于科研成果。
10. 不得根据实验运行状态推断实验成功或失败，更不能据此直接否定 Idea。

## 五、信息不足与空章节处理

- 如果某个章节没有对应活动记录，写「本周期无记录（当前活动记录未覆盖该方向，不代表实际未开展）」。
- 如果只有操作记录，没有研究内容，应如实说明记录能够支持的事实。
- 如果存在研究过程但没有最终结论，应重点报告过程、当前观察和待解决问题。
- 不得使用泛化套话填充空章节。
- 不得为了使周报看起来丰富而虚构研究进展。
- 不要把没有记录直接解释为没有开展工作。

当文献、理论、实验等方向均缺少记录时，应在第一节的总体概述中简要说明本周记录覆盖情况，避免读者误以为这些方向已经被充分总结。

## 六、语言与表达风格

采用正式、客观、专业的科研汇报语言。

具体要求：

- 结论优先，过程辅助。
- 重点突出，层次清晰。
- 使用准确的学术术语和工程技术表达。
- 尽量使用具体动作、具体结果和具体判断。
- 不使用空泛的评价性语言，例如「取得了显著进展」「进行了深入研究」，除非有充分依据。
- 不使用流水账式表达。
- 不重复相同的事实。
- 不使用第一人称承诺。
- 不夸大研究成果，不掩盖尚未解决的问题。

## 七、输出格式

严格使用以下七节结构：

1. 本周工作内容
2. 文献调研进展
3. 理论研究进展
4. 实验工作与结果
5. Idea 进展
6. 遇到的问题
7. 下周计划

输出要求：

- 使用中文。
- 开头为项目名称和报告周期。
- 必须保留七个章节及其原有顺序。
- 第一节允许先写一段本周研究工作概述。
- 章节内部只使用简短段落和无序列表。
- 不使用三级标题、表格、代码块或引用块。
- 每节最多 12 条项目符号。
- 每条项目符号不超过 120 字。
- 相同研究事项只总结一次，避免跨章节重复。
- 只输出完整的 Markdown 周报，不添加前言、写作说明或报告之外的评论。

最终目标：生成一份能够真实反映科研人员一周研究活动的工作报告，使读者不仅知道「做了什么」，还能够理解「为什么做、怎么做、得到了什么、目前如何判断，以及接下来准备怎么做」。`
}

const PLUGIN_SKILL_PATTERN = /^plugin:[a-z0-9][a-z0-9_-]*:[A-Za-z0-9][A-Za-z0-9._-]*$/

/**
 * The workspace skill store plus the plugin-provided registry. Plugin skills
 * arrive through `registerSkill` from the plugin host and are read-only here;
 * their lifetime is the host's.
 */
export class SkillRegistry {
  private readonly settings: SettingsStore
  private readonly clock: Clock
  private readonly plugin = new Map<string, WeeklySkill>()

  constructor(options: { settings: SettingsStore; clock: Clock; pluginSkills?: WeeklySkill[] }) {
    this.settings = options.settings
    this.clock = options.clock
    for (const skill of options.pluginSkills ?? []) this.registerSkill(skill)
  }

  list(): SkillSet {
    const stored = this.loadStored()
    const skills: WeeklySkill[] = [
      BUILTIN_SKILL,
      ...stored.map((skill) => ({ ...skill, source: 'custom' as const })),
      ...[...this.plugin.values()]
    ]
    const wanted = this.readDefault()
    const selected = wanted === '' || wanted === BUILTIN_SKILL_ID ? '' : wanted
    const set: SkillSet = { skills, default: selected }
    if (selected !== '' && !skills.some((skill) => skill.id === selected)) set.default = ''
    return set
  }

  /** Creates or updates one user skill; an empty id creates. */
  save(input: SkillInput): SkillSet {
    const name = input.name.trim()
    const prompt = input.prompt.trim()
    if (name === '') this.invalid('Skill 需要一个名称')
    if (Array.from(name).length > MAX_SKILL_NAME_RUNES) {
      this.invalid(`Skill 名称超过 ${MAX_SKILL_NAME_RUNES} 字`)
    }
    if (prompt === '') this.invalid('Skill 内容不能为空')
    if (Buffer.byteLength(prompt, 'utf8') > MAX_SKILL_PROMPT_BYTES) {
      this.invalid(`Skill 内容超过 ${MAX_SKILL_PROMPT_BYTES >> 10} KiB`)
    }
    const stored = this.loadStored()
    const id = input.id?.trim() ?? ''
    if (id === '') {
      if (stored.length >= MAX_SKILLS) this.invalid(`最多保留 ${MAX_SKILLS} 个 Skill`)
      stored.push({ id: newId(this.clock), name, prompt })
    } else if (id === BUILTIN_SKILL_ID) {
      this.invalid('内置 Skill 不可修改；请复制成新 Skill 再编辑')
    } else if (this.plugin.has(id)) {
      this.invalid(`插件 Skill ${id} 不可修改`)
    } else {
      const index = stored.findIndex((skill) => skill.id === id)
      if (index < 0) throw new AppError(AppErrorCode.NotFound, 'Skill 不存在', { skillId: id })
      stored[index] = { id, name, prompt }
    }
    this.writeStored(stored)
    return this.list()
  }

  /** Deletes a user skill; deleting the default falls back to the builtin. */
  remove(id: string): SkillSet {
    if (id === BUILTIN_SKILL_ID) this.invalid('内置 Skill 不可删除')
    if (this.plugin.has(id)) this.invalid(`插件 Skill ${id} 不可删除`)
    const stored = this.loadStored()
    const kept = stored.filter((skill) => skill.id !== id)
    if (kept.length === stored.length) {
      throw new AppError(AppErrorCode.NotFound, 'Skill 不存在', { skillId: id })
    }
    this.writeStored(kept)
    if (this.readDefault() === id) this.settings.delete(DEFAULT_SKILL_KEY)
    return this.list()
  }

  /** Selects the default skill; empty or builtin means the shipped one. */
  setDefault(id: string): SkillSet {
    if (id !== '' && id !== BUILTIN_SKILL_ID && !this.list().skills.some((skill) => skill.id === id)) {
      throw new AppError(AppErrorCode.NotFound, 'Skill 不存在', { skillId: id })
    }
    if (id === '' || id === BUILTIN_SKILL_ID) this.settings.delete(DEFAULT_SKILL_KEY)
    else this.settings.set(DEFAULT_SKILL_KEY, id)
    return this.list()
  }

  /** Registers a plugin-provided skill; ids are `plugin:<plugin>:<file>`. */
  registerSkill(skill: WeeklySkill): void {
    if (!PLUGIN_SKILL_PATTERN.test(skill.id)) {
      this.invalid('插件 Skill ID 必须形如 plugin:<plugin-id>:<file>')
    }
    if (this.plugin.has(skill.id)) this.invalid(`插件 Skill ${skill.id} 已注册`)
    const name = skill.name.trim()
    const prompt = skill.prompt.trim()
    if (name === '' || prompt === '') this.invalid('插件 Skill 需要名称与内容')
    if (Array.from(name).length > MAX_SKILL_NAME_RUNES) {
      this.invalid(`Skill 名称超过 ${MAX_SKILL_NAME_RUNES} 字`)
    }
    if (Buffer.byteLength(prompt, 'utf8') > MAX_SKILL_PROMPT_BYTES) {
      this.invalid(`Skill 内容超过 ${MAX_SKILL_PROMPT_BYTES >> 10} KiB`)
    }
    this.plugin.set(skill.id, { ...skill, name, prompt, source: 'plugin', builtin: false })
  }

  unregisterPluginSkill(id: string): void {
    this.plugin.delete(id)
  }

  /** The skill one generation runs with; unknown explicit ids are rejected. */
  resolve(id?: string): WeeklySkill {
    const set = this.list()
    const wanted = id === undefined || id.trim() === '' ? set.default : id.trim()
    if (wanted === '' || wanted === BUILTIN_SKILL_ID) return BUILTIN_SKILL
    const found = set.skills.find((skill) => skill.id === wanted && skill.id !== BUILTIN_SKILL_ID)
    if (found === undefined) {
      throw new AppError(AppErrorCode.InvalidArgument, `Skill 不存在：${wanted}`, { skillId: wanted })
    }
    return found
  }

  private loadStored(): Array<{ id: string; name: string; prompt: string }> {
    const raw = this.settings.get(SKILLS_KEY)
    if (raw === undefined || raw.trim() === '') return []
    let parsed: unknown
    try {
      parsed = JSON.parse(raw)
    } catch {
      this.invalid('Skill 配置损坏，请重新保存')
    }
    if (!Array.isArray(parsed)) this.invalid('Skill 配置损坏，请重新保存')
    const out: Array<{ id: string; name: string; prompt: string }> = []
    for (const entry of parsed) {
      if (typeof entry !== 'object' || entry === null) continue
      const record = entry as Record<string, unknown>
      const id = typeof record.id === 'string' ? record.id : ''
      const name = typeof record.name === 'string' ? record.name : ''
      const prompt = typeof record.prompt === 'string' ? record.prompt : ''
      if (id === '' || id === BUILTIN_SKILL_ID || name.trim() === '') continue
      out.push({ id, name, prompt })
    }
    return out
  }

  private writeStored(skills: Array<{ id: string; name: string; prompt: string }>): void {
    this.settings.set(SKILLS_KEY, JSON.stringify(skills))
  }

  private readDefault(): string {
    return this.settings.get(DEFAULT_SKILL_KEY)?.trim() ?? ''
  }

  private invalid(message: string): never {
    throw new AppError(AppErrorCode.InvalidArgument, message, { field: 'skill' })
  }
}
