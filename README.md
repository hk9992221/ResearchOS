# ResearchOS

**A focused desktop workspace for research, from literature and ideas to experiments and weekly reviews.**

[简体中文](README.zh-CN.md) · [Releases](https://github.com/hk9992221/ResearchOS/releases) · [License](LICENSE)

ResearchOS brings the scattered parts of a research project into one workspace. Organize papers, develop ideas, write notes, track experiments and decisions, and review your work over time without forcing research into separate project types.

> **Release status:** 1.0.0 is the planned first distribution. Windows, macOS, and Linux packages are subject to release-build and installation checks. Screenshots and verified platform-specific requirements will be added after validation.

## What you can do

| Area | Capability |
| --- | --- |
| **Projects & workspaces** | Manage projects within a unified workspace and navigate their related research materials. |
| **Literature** | Work with a literature collection, Zotero integration, paper details, and an in-app PDF reader. |
| **Research notes** | Write and organize research notes with Markdown and mathematical notation. |
| **Ideas** | Capture ideas and connect them to ongoing project work. |
| **Experiments** | Track experiments and runs, their metadata and results, with Git-related provenance features. |
| **Kanban** | Organize research tasks and follow their progress. |
| **Weekly review** | Generate and revise periodic research reports based on recorded activity. |
| **Plugins & AI** | Manage plugins and use optional AI-backed workflows where a provider is configured. |

Features may require configuration or external services. The presence of a module does not imply that every integration has been tested across all supported operating systems.

## Downloads

Release packages will be available from **[GitHub Releases](https://github.com/hk9992221/ResearchOS/releases)** after the draft release has been reviewed and published.

| Platform | Architecture | Installer | Installer-free package |
| --- | --- | --- | --- |
| Windows | x64 | NSIS `.exe` | Portable `.exe` |
| macOS | Intel x64 | `.dmg` | `.zip` containing `.app` |
| macOS | Apple Silicon arm64 | `.dmg` | `.zip` containing `.app` |
| Linux | x64 | `.deb` / `.rpm` | `.AppImage` |

Each published release will include `SHA256SUMS.txt` for file integrity checks. On macOS and Windows, initial builds may not be signed or notarized, and operating-system security warnings may appear. **Portable / installer-free does not mean that settings or workspaces are stored beside the application binary.** Keep a backup of your research workspace when upgrading.

## Getting started

1. Download the package appropriate for your platform and CPU architecture.
2. Install it or run the installer-free build.
3. Open ResearchOS and choose or create a research workspace.
4. Add a project, then start with literature, notes, ideas, or experiments.
5. Optionally configure AI services, Zotero integration, or plugins as needed.

## Development

**Requirements:** Node.js 22+, pnpm 10.11.0, and the native build toolchain appropriate to your operating system. The application uses native Node addons (including `better-sqlite3`), so platform-specific build prerequisites may apply.

```bash
git clone https://github.com/hk9992221/ResearchOS.git
cd ResearchOS
pnpm install --frozen-lockfile
pnpm dev
```

Other scripts:

```bash
pnpm build        # Build shared/core packages and the desktop app
pnpm typecheck    # TypeScript verification
pnpm test         # Run tests, when test files are present
```

### Repository layout

```text
packages/
  shared/        Shared contracts, entities, IPC and plugin API
  core/          Workspace, data, research modules and services
  desktop/       Electron main/preload process and React UI
resources/       Application artwork
scripts/         Development and verification tools
```

The project uses Electron, TypeScript, React, pnpm workspaces, SQLite, and related libraries. The build is handled with electron-vite and electron-builder.

## Releases

Release packaging is manually triggered through GitHub Actions. **Build Only** validates and stores artifacts without creating a GitHub Release. **Draft Release** requires the platform builds to pass, then creates an unpublished release with the download packages, checksums and release notes. The maintainer reviews the draft before publishing.

For the initial `1.0.0` release, the existing tag is authoritative. The workflow builds the tagged source with its matching version metadata; it does **not** move, overwrite, or rewrite the tag.

## Project status and contributions

ResearchOS is an actively developed project. Platform support, integrations, and packaging should be assessed against the published release notes and known issues. Issues and contributions can be discussed through this repository when the maintainer enables public collaboration.

## License

Copyright © 2026 **hk9992221**.

ResearchOS is distributed under the [Apache License, Version 2.0](LICENSE). See [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md) and [licenses/](licenses/) for bundled dependency notices and license terms.
