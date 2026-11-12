#!/usr/bin/env bash
# 将需求与 SPEC 文档快照到本地孤儿分支 docs-local（永不推送）。
# 主分支继续忽略这些文件：文件始终留在磁盘，推送主分支不会带上文档。
# 用法: bash scripts/snapshot-docs.sh ["提交说明"]
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

BRANCH=docs-local
MSG="${1:-docs: 本地文档快照}"

# 快照专用身份（仅环境变量，不改 git 配置；已有环境变量优先）
export GIT_AUTHOR_NAME="${GIT_AUTHOR_NAME:-ResearchOS Docs Snapshot}"
export GIT_AUTHOR_EMAIL="${GIT_AUTHOR_EMAIL:-docs@researchos.local}"
export GIT_COMMITTER_NAME="${GIT_COMMITTER_NAME:-ResearchOS Docs Snapshot}"
export GIT_COMMITTER_EMAIL="${GIT_COMMITTER_EMAIL:-docs@researchos.local}"
IDX="$(git rev-parse --git-dir)/docs-snapshot-index"

export GIT_INDEX_FILE="$IDX"
rm -f "$IDX"
if git rev-parse --verify --quiet "refs/heads/$BRANCH" >/dev/null; then
  git read-tree "refs/heads/$BRANCH"
  PARENT_ARGS=(-p "$(git rev-parse "refs/heads/$BRANCH")")
else
  git read-tree --empty
  PARENT_ARGS=()
fi
git add -f -- docs/功能说明.md docs/specs
TREE="$(git write-tree)"
COMMIT="$(git commit-tree "$TREE" "${PARENT_ARGS[@]}" -m "$MSG")"
git update-ref "refs/heads/$BRANCH" "$COMMIT"
rm -f "$IDX"
unset GIT_INDEX_FILE

# 护栏：在该分支上默认 push 会因远端不存在而失败（仍需避免显式 push 与 --all/--mirror）
git config "branch.$BRANCH.pushRemote" no-push

echo "已快照到本地分支 $BRANCH: $COMMIT"
echo "注意: 不要推送 $BRANCH；避免 git push --all / --mirror。"