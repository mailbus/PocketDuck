#!/usr/bin/env bash
# Release 五段全链（规格 v0x 收口）：MCP查询 → apply固化 → 定时执行 → 运行历史 → 飞书到达。
# 前置: PDUCK_FEISHU_WEBHOOK=<飞书自定义机器人URL>（到达后请人工截图留证）
# 用法: PDUCK_FEISHU_WEBHOOK=<url> scripts/test_release.sh   （约 4-5 分钟）
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -z "${PDUCK_FEISHU_WEBHOOK:-}" ]; then
  echo "需要 PDUCK_FEISHU_WEBHOOK 环境变量（飞书自定义机器人 webhook）"; exit 1
fi
export PDUCK_FEISHU_WEBHOOK

echo "═══ 段1+2: MCP 查询 / apply 固化（scripts/test_mcp.sh + test_apply.sh）═══"
bash scripts/test_mcp.sh
bash scripts/test_apply.sh

echo "═══ 段3+4+5: 定时执行 / 运行历史 / 飞书到达（scripts/test_pipeline.sh + test_push.sh）═══"
bash scripts/test_pipeline.sh
bash scripts/test_push.sh

echo "═══ Release 五段全链收口完成——飞书群里应有 2 条 pduck 消息，请截图留证 ═══"
