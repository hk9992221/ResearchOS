# ResearchOS

**面向科研全过程的桌面工作台：从文献、想法，到实验与每周回顾。**

[English](README.md) · [版本发布](https://github.com/hk9992221/ResearchOS/releases) · [许可证](LICENSE)

ResearchOS 希望把分散在多个工具中的科研活动组织进统一的工作空间。你可以在同一个项目中管理文献、记录想法、撰写笔记、追踪实验与决策，并定期回顾研究进展，而不需要预先把项目划分为“理论型”或“实验型”。

> **发布状态：** 1.0.0 是计划中的首次发行版本。Windows、macOS 和 Linux 安装包仍需经过实际 CI 构建与安装验证。界面截图及经验证的系统要求将在测试后补充。

## 核心功能

| 模块 | 主要能力 |
| --- | --- |
| **Project / Workspace** | 在统一工作空间中组织项目及其相关科研资料。 |
| **Literature** | 管理文献资料，使用 Zotero 集成、论文详情及应用内 PDF 阅读器。 |
| **Research Notes** | 使用 Markdown 和数学公式记录研究内容。 |
| **Ideas** | 捕捉研究思路，并与项目工作关联。 |
| **Experiments** | 管理实验与 Run，记录元数据、结果及 Git 相关追溯信息。 |
| **Kanban** | 组织科研任务，追踪工作进度。 |
| **Weekly Review** | 根据已有活动记录生成、修订周期性科研周报。 |
| **Plugins / AI** | 管理插件，并在配置了模型服务时使用相关 AI 功能。 |

部分功能需要额外配置或第三方服务；仓库中存在相应模块，不代表所有平台上的集成能力都已经通过测试。

## 下载安装

审核并发布 Draft Release 后，可从 **[GitHub Releases](https://github.com/hk9992221/ResearchOS/releases)** 下载对应文件。

| 平台 | 架构 | 安装包 | 免安装包 |
| --- | --- | --- | --- |
| Windows | x64 | NSIS `.exe` | Portable `.exe` |
| macOS | Intel x64 | `.dmg` | 含 `.app` 的 `.zip` |
| macOS | Apple Silicon arm64 | `.dmg` | 含 `.app` 的 `.zip` |
| Linux | x64 | `.deb` / `.rpm` | `.AppImage` |

每个正式发布版本附有 `SHA256SUMS.txt`，用于校验下载文件完整性。首次构建可能尚未完成 Windows 代码签名、macOS 签名与公证，因此系统可能提示安全警告。**“免安装”不等于“配置和科研数据随可执行文件移动”。** 升级前建议备份科研工作空间。

## 快速开始

1. 根据操作系统和处理器架构下载合适的发行包。
2. 完成安装，或直接运行免安装版。
3. 启动 ResearchOS，选择或创建科研工作空间。
4. 创建 Project，从文献、笔记、Ideas 或 Experiments 开始记录。
5. 根据需要配置 AI 服务、Zotero 集成及插件。

## 本地开发

**环境要求：** Node.js 22+、pnpm 10.11.0，以及适配操作系统的原生编译工具链。项目依赖 `better-sqlite3` 等原生模块，部分环境需要额外的编译依赖。

```bash
git clone https://github.com/hk9992221/ResearchOS.git
cd ResearchOS
pnpm install --frozen-lockfile
pnpm dev
```

常用命令：

```bash
pnpm build        # 编译 shared/core 包与桌面应用
pnpm typecheck    # TypeScript 类型检查
pnpm test         # 存在测试文件时执行测试
```

### 仓库结构

```text
packages/
  shared/        公共契约、实体、IPC 与插件 API
  core/          工作空间、数据层、科研模块与服务
  desktop/       Electron 主进程/预加载脚本和 React 界面
resources/       应用图标等资源
scripts/         开发与校验工具
```

主要技术包括 Electron、TypeScript、React、pnpm Workspace、SQLite；构建工具为 electron-vite 与 electron-builder。

## 版本发布

GitHub Actions 仅手动触发发行构建。**Build Only** 只检查并保存构建产物，不创建 GitHub Release；**Draft Release** 要求全部平台构建成功后创建未公开发布的草稿，上传安装包、校验文件和 Release Notes，由维护者审核后手动发布。

首次 `1.0.0` 以现有 Tag 为准。发布 Workflow 使用该 Tag 对应的应用源码和版本声明，**不移动、覆盖或改写原 Tag**。

## 项目状态与贡献

ResearchOS 仍在持续开发中。具体平台支持情况、集成限制和已知问题，请以实际 Release Notes 为准。仓库开放协作后，可通过 Issues 和 Pull Requests 参与讨论及贡献。

## 许可证

Copyright © 2026 **hk9992221**。

ResearchOS 基于 [Apache License 2.0](LICENSE) 发布。第三方依赖的许可证及声明见 [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md) 与 [licenses/](licenses/) 目录。
