#!/usr/bin/env bash
# 票10 e2e：apply 校验错误→自修复→幂等 upsert→view 出真数据→跨会话复用。
# 用法: scripts/test_apply.sh（自动起 serve，结束清理）
set -euo pipefail
cd "$(dirname "$0")/.."
export PYTHONIOENCODING=utf-8

SERVE_URL=http://127.0.0.1:8090
TMP=$(mktemp -d)
trap 'kill ${SERVE_PID:-} 2>/dev/null || true; rm -rf "$TMP"' EXIT

if ! curl -s "$SERVE_URL/api/health" > /dev/null 2>&1; then
  ./pduck.exe serve --http=127.0.0.1:8090 > /tmp/pduck_serve.log 2>&1 &
  SERVE_PID=$!
  for i in $(seq 1 20); do curl -s "$SERVE_URL/api/health" > /dev/null 2>&1 && break; sleep 0.5; done
fi
./pduck.exe superuser upsert admin@example.com yourpassword > /dev/null 2>&1
TOKEN=$(curl -s "$SERVE_URL/api/collections/_superusers/auth-with-password" \
  -H "Content-Type: application/json" \
  -d '{"identity":"admin@example.com","password":"yourpassword"}' \
  | python -c "import json,sys;print(json.load(sys.stdin)['token'])")

# ── 0. 清场上次产物（保证 created 语义可判定）
python scripts/pb_clean.py "$SERVE_URL" "$TOKEN" saved_queries views pipelines > /dev/null

# ── 1. 坏 YAML → 结构化错误（agent 自修复前）
cat > "$TMP/bad.yaml" <<'EOF'
kind: named_query
name: borough-fare
description: 缺 sql 字段
EOF
set +e
OUT_BAD=$(./pduck.exe apply -f "$TMP/bad.yaml" -token "$TOKEN" 2>/dev/null)
RC_BAD=$?
set -e
echo "$OUT_BAD" | python -c "
import json,sys
d=json.load(sys.stdin)
assert d['ok'] is False and any('sql' in e['message'] for e in d['errors']), d
print('PASS 坏 YAML → 结构化错误 (exit code 见下)')
"
[ "$RC_BAD" = "2" ] && echo "PASS 退出码 2"

# ── 2. 自修复后的 named query → created；重复 apply → updated（幂等）
cat > "$TMP/query.yaml" <<'EOF'
kind: named_query
name: borough-fare
sql: SELECT z.Borough, ROUND(SUM(t.fare_amount),2) AS total_fare FROM read_parquet('data/yellow_tripdata_2025-01.parquet') t JOIN sqlite_scan('testdata/zones.sqlite','zones') z ON t.PULocationID = z.LocationID WHERE z.Borough != 'Unknown' GROUP BY 1 ORDER BY 2 DESC
description: 每 Borough 总车费
EOF
A1=$(./pduck.exe apply -f "$TMP/query.yaml" -token "$TOKEN")
echo "$A1" | python -c "import json,sys; d=json.load(sys.stdin); assert d['ok'] and d['action']=='created', d; print('PASS apply →', d['action'])"
A2=$(./pduck.exe apply -f "$TMP/query.yaml" -token "$TOKEN")
echo "$A2" | python -c "import json,sys; d=json.load(sys.stdin); assert d['ok'] and d['action']=='updated', d; print('PASS 重复 apply 幂等 →', d['action'])"

# ── 3. view YAML（引用上面的 named query）
cat > "$TMP/view.yaml" <<'EOF'
kind: view
name: borough-fare-bar
query: borough-fare
chart: {type: bar, x: Borough, y: total_fare, title: Borough 总车费}
EOF
./pduck.exe apply -f "$TMP/view.yaml" -token "$TOKEN" | python -c "import json,sys; d=json.load(sys.stdin); assert d['ok'] and d['action']=='created', d; print('PASS view apply')"

# ── 4. 两个独立 MCP 会话（模拟跨会话复用）：会话A get_view 真数据；会话B run_named_query 复用
mcp_call() { # $1=会话标识(仅日志), 其余=JSON-RPC 行
  {
    printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}'
    printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized"}'
    for line in "$@"; do printf '%s\n' "$line"; done
    sleep 3
  } | ./pduck.exe mcp -token "$TOKEN"
}

VIEW_OUT=$(mcp_call '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_view","arguments":{"name":"borough-fare-bar"}}}' | grep '"id":3')
echo "$VIEW_OUT" | python -c "
import json,sys
m=json.load(sys.stdin)
d=json.loads(m['result']['content'][0]['text'])
assert d['view']['chart']['type']=='bar' and d['view']['chart']['x']=='Borough', d['view']
assert d['data']['row_count']>=5 and 'total_fare' in d['data']['columns'], d['data']
print('PASS 会话A get_view 真数据:', d['view']['chart']['title'], '| rows:', d['data']['row_count'])
"

NQ_OUT=$(mcp_call '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"run_named_query","arguments":{"name":"borough-fare"}}}' | grep '"id":3')
echo "$NQ_OUT" | python -c "
import json,sys
m=json.load(sys.stdin)
d=json.loads(m['result']['content'][0]['text'])
assert d['row_count']>=5, d
print('PASS 会话B run_named_query 复用固化产物: rows', d['row_count'], f'{d[\"elapsed_ms\"]}ms')
"

# ── 5. pipeline YAML 只存不执行
cat > "$TMP/pipeline.yaml" <<'EOF'
kind: pipeline
name: borough-fare-weekly
schedule: "0 8 * * 1"
tasks:
  - query: borough-fare
  - export: to=borough_fare.xlsx
EOF
./pduck.exe apply -f "$TMP/pipeline.yaml" -token "$TOKEN" | python -c "import json,sys; d=json.load(sys.stdin); assert d['ok'], d; print('PASS pipeline 存储（票11 才执行）')"

echo "票10 e2e 全部过线"
