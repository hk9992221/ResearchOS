# AGENTS.md — ResearchOS 协作约定

## 1. Worktree 位置

本仓库所有 git worktree 必须建在 `F:/projects/ResearchOS-Worktree/` 下，
目录名即任务名，例如：

```bash
git -C F:/projects/ResearchOS worktree add F:/projects/ResearchOS-Worktree/spec-01-foundation -b feat/spec-01-foundation
```

不要散落在 `F:/projects/` 的其他位置。主检出（`F:/projects/ResearchOS`）只用于保持 `main` 干净。

## 2. docs/ 不进仓库

`docs/` 下的需求与 SPEC 文档只存在于磁盘和本地孤儿分支 `docs-local`（由
`scripts/snapshot-docs.sh` 生成快照）。`.gitignore` 已忽略 `docs/specs/` 与
`docs/功能说明.md`；其余任何 `docs/` 内容也**不得**被 `git add` 或提交进 `main`。

## 3. 本地 CI

唯一的本地质量门禁是：

```bash
bash scripts/ci.sh
```

依次执行 gofmt 检查（不干净则列出文件并失败）、`go vet ./...`、`go build ./...`、
`go test ./... -count=1`，最后打印 `CI OK`。模块内还没有任何 Go 包时会跳过
vet/build/test。GitHub Actions（`.github/workflows/ci.yml`）运行的也是这个脚本，
所以本地通过即视为 CI 通过。