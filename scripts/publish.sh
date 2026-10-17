#!/usr/bin/env bash
# 发布到 GitHub 公共仓库：克隆 → 剔除私有文件 → 强推。
# 每次发布历史哈希都会重写（filter-repo 确定性重写，前缀哈希不变）。
# 用法: scripts/publish.sh <github仓库URL>
set -euo pipefail
cd "$(dirname "$0")/.."

URL="${1:?用法: scripts/publish.sh <github仓库URL>}"
MIRROR=.publish-mirror

# 私有文件清单（新增私有文档时在此追加）
PRIVATE_PATHS=(
  --path CLAUDE.md
  --path docs/pduck讨论.md
  --path docs/项目启动手册.md
  --path docs/规格-v0.md
  --path-regex '^docs/实验记录-'
  --path .scratch
)

[ -d "$MIRROR" ] || git clone --no-local --no-hardlinks . "$MIRROR"
cd "$MIRROR"
git fetch --no-tags ../ main
LAN_HEAD=$(git rev-parse FETCH_HEAD)
git reset --hard FETCH_HEAD

python -m git_filter_repo --force --invert-paths "${PRIVATE_PATHS[@]}"

# 发布前自检：任何提交里都不允许残留私有文件
if git -c core.quotepath=false log --all --name-only --pretty=format: \
   | grep -qE 'CLAUDE\.md|pduck讨论|项目启动手册|规格-v0|实验记录|^\.scratch/'; then
  echo "错误: 过滤后仍有私有文件残留，中止发布" >&2
  exit 1
fi

# 本机 git 凭据可能与 gh 账号不一致，用 gh token 组认证 URL（无 gh 则原样推）
if TOKEN=$(gh auth token 2>/dev/null); then
  URL=$(echo "$URL" | sed "s|https://|https://x-access-token:${TOKEN}@|")
fi
git remote add github "$URL" 2>/dev/null || git remote set-url github "$URL"
git push --force github HEAD:main
echo "已发布到 https://github.com/${URL#*github.com/}（main）"

# 同步指向局域网 main HEAD 的 tag：过滤重写了历史，须在镜像里重建后推送（触发 Release 构建）
cd ..
for t in $(git tag --points-at "$LAN_HEAD"); do
  git -C "$MIRROR" tag -f "$t" HEAD
  git -C "$MIRROR" push --force github "refs/tags/$t"
  echo "tag $t 已同步（触发 Release）"
done
