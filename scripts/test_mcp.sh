#!/usr/bin/env bash
# 票09 e2e：真 serve + saved_queries 种子 + pduck mcp stdio 全会话（run_sql/run_named_query/空态）。
# 用法: scripts/test_mcp.sh   （自动起 serve，结束时清理）
set -euo pipefail
cd "$(dirname "$0")/.."
export PYTHONIOENCODING=utf-8

SERVE_URL=http://127.0.0.1:8090

# 1. serve（已在跑则复用）
if ! curl -s "$SERVE_URL/api/health" > /dev/null 2>&1; then
  ./pduck.exe serve --http=127.0.0.1:8090 > /tmp/pduck_serve.log 2>&1 &
  SERVE_PID=$!
  trap 'kill $SERVE_PID 2>/dev/null || true' EXIT
  for i in $(seq 1 20); do
    curl -s "$SERVE_URL/api/health" > /dev/null 2>&1 && break
    sleep 0.5
  done
  echo "serve 已启动 (pid $SERVE_PID)"
else
  echo "serve 已在跑，复用"
fi

# 2. superuser + token
./pduck.exe superuser upsert admin@example.com yourpassword > /dev/null 2>&1
TOKEN=$(curl -s "$SERVE_URL/api/collections/_superusers/auth-with-password" \
  -H "Content-Type: application/json" \
  -d '{"identity":"admin@example.com","password":"yourpassword"}' \
  | python -c "import json,sys;print(json.load(sys.stdin)['token'])")
[ -n "$TOKEN" ] && echo "token ok"
python scripts/pb_clean.py "$SERVE_URL" "$TOKEN" saved_queries views pipelines pipeline_runs > /dev/null

# 3. 种子 named query（幂等：存在则跳过）
EXIST=$(curl -s "$SERVE_URL/api/collections/saved_queries/records?filter=%28name%3D%27demo-total-trips%27%29" -H "Authorization: $TOKEN")
if ! echo "$EXIST" | grep -q '"totalItems":1'; then
  curl -s -X POST "$SERVE_URL/api/collections/saved_queries/records" \
    -H "Authorization: $TOKEN" -H "Content-Type: application/json" \
    -d '{"name":"demo-total-trips","description":"e2e seed","sql":"SELECT COUNT(*) AS trips, ROUND(SUM(fare_amount),2) AS total_fare FROM read_parquet('"'"'data/yellow_tripdata_2025-01.parquet'"'"')"}' > /dev/null
  echo "种子 named query 已建"
fi

# 4. MCP stdio 会话
OUT=/tmp/mcp_e2e_out.jsonl
{
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}'
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
  printf '%s\n' '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"run_sql","arguments":{"sql":"SELECT COUNT(*) AS n FROM read_parquet('"'"'data/yellow_tripdata_2025-01.parquet'"'"')"}}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"run_named_query","arguments":{"name":"demo-total-trips"}}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"get_schedule_results","arguments":{}}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"get_view","arguments":{"name":"fare"}}}'
  sleep 5
} | ./pduck.exe mcp -token "$TOKEN" > "$OUT" 2>/tmp/mcp_e2e_err.log

# 5. 判定
python - "$OUT" <<'EOF'
import json, sys
msgs = {}
for line in open(sys.argv[1], encoding="utf-8"):
    line = line.strip()
    if not line or not line.startswith("{"):
        continue
    m = json.loads(line)
    if "id" in m:
        msgs[m["id"]] = m

def result_text(mid):
    r = msgs[mid]["result"]
    return r["content"][0]["text"]

tools = [t["name"] for t in msgs[2]["result"]["tools"]]
assert set(tools) == {"run_sql", "run_named_query", "get_schedule_results", "get_view"}, tools
print("PASS tools/list:", tools)

r3 = json.loads(result_text(3))
assert r3["row_count"] == 1 and r3["rows"][0][0] > 1_000_000, r3
print("PASS run_sql 真数据:", r3["rows"][0], f'{r3["elapsed_ms"]}ms')

r4 = json.loads(result_text(4))
assert r4["rows"][0][0] == r3["rows"][0][0], (r3["rows"], r4["rows"])
print("PASS run_named_query 与 run_sql 对账一致:", r4["rows"][0])

r5 = json.loads(result_text(5))
assert r5.get("results") == [], r5
print("PASS get_schedule_results 空态:", r5["note"])

r6 = json.loads(result_text(6))
assert r6.get("view") is None, r6
print("PASS get_view 空态:", r6["note"])

print("票09 e2e 全部过线")
EOF
