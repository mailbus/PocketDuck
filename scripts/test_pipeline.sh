#!/usr/bin/env bash
# 票11 e2e：worker 到点自动执行 → 快照对账 → pduck run 手动一致 → 重启后调度恢复。
# 用法: scripts/test_pipeline.sh（总时长约 3-4 分钟：cron 最小粒度 1 分钟）
set -euo pipefail
cd "$(dirname "$0")/.."
export PYTHONIOENCODING=utf-8

SERVE_URL=http://127.0.0.1:8090
TMP=$(mktemp -d)
OUT_WIN=$(cygpath -m "$PWD")/.scratch  # Windows 原生路径（export 由 pduck.exe 进程打开）
trap 'kill ${SERVE_PID:-} 2>/dev/null || true; rm -rf "$TMP" .scratch/e2e_zone_count.xlsx' EXIT

start_serve() {
  ./pduck.exe serve --http=127.0.0.1:8090 > /tmp/pduck_serve.log 2>&1 &
  SERVE_PID=$!
  for i in $(seq 1 20); do curl -s "$SERVE_URL/api/health" > /dev/null 2>&1 && return; sleep 0.5; done
  echo "serve 启动失败"; exit 1
}

start_serve
./pduck.exe superuser upsert admin@example.com yourpassword > /dev/null 2>&1
TOKEN=$(curl -s "$SERVE_URL/api/collections/_superusers/auth-with-password" \
  -H "Content-Type: application/json" \
  -d '{"identity":"admin@example.com","password":"yourpassword"}' \
  | python -c "import json,sys;print(json.load(sys.stdin)['token'])")

python scripts/pb_clean.py "$SERVE_URL" "$TOKEN" saved_queries views pipelines pipeline_runs > /dev/null

# ── 1. 固化 named query + 每分钟管道（query + export xlsx）
cat > "$TMP/query.yaml" <<'EOF'
kind: named_query
name: zone-count
sql: SELECT z.Borough, COUNT(*) AS trips FROM read_parquet('data/yellow_tripdata_2025-01.parquet') t JOIN sqlite_scan('testdata/zones.sqlite','zones') z ON t.PULocationID = z.LocationID GROUP BY 1 ORDER BY 2 DESC
EOF
cat > "$TMP/pipeline.yaml" <<EOF
kind: pipeline
name: zone-count-minutely
schedule: "* * * * *"
tasks:
  - query: zone-count
  - export: to=$OUT_WIN/e2e_zone_count.xlsx
EOF
./pduck.exe apply -f "$TMP/query.yaml" -f "$TMP/pipeline.yaml" -token "$TOKEN" | python -c "import json,sys;[print(json.loads(l)['artifact'], json.loads(l)['action']) for l in sys.stdin if l.strip()]"

# ── 2. 等 worker 到点自动执行（cron 最小粒度 1 分钟 + 30s 轮询 → 最多等 ~100s）
echo "等待 worker 到点自动执行（最多 100s）..."
for i in $(seq 1 100); do
  HIST=$(curl -s "$SERVE_URL/api/collections/pipeline_runs/records?filter=%28pipeline%3D%27zone-count-minutely%27%29" -H "Authorization: $TOKEN")
  echo "$HIST" | python -c "
import json,sys
d=json.load(sys.stdin)
items=[r for r in d.get('items',[]) if r['status']=='success']
sys.exit(0 if items else 1)
" && break
  sleep 1
  [ "$i" = "100" ] && { echo "FAIL: 100s 内 worker 未自动执行"; exit 1; }
done
echo "PASS worker 到点自动执行"

# ── 3. 快照对账：checksum 与文件 sha256 一致；行数一致
HIST=$(curl -s "$SERVE_URL/api/collections/pipeline_runs/records?filter=%28pipeline%3D%27zone-count-minutely%27%29" -H "Authorization: $TOKEN")
echo "$HIST" | python -c "
import json,sys,hashlib
d=json.load(sys.stdin)
r=d['items'][0]
assert r['status']=='success', r
h=hashlib.sha256(open(r['snapshot'],'rb').read()).hexdigest()
assert h==r['checksum'], (h, r['checksum'])
print('PASS 快照 checksum 一致:', r['checksum'][:16], '| rows:', r['rows'], f\"| {r['elapsed_ms']}ms\")
"
[ -f ".scratch/e2e_zone_count.xlsx" ] && echo "PASS export 任务产出 xlsx"

# ── 4. MCP get_schedule_results 出真数据
{
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}'
  printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized"}'
  printf '%s\n' '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_schedule_results","arguments":{"name":"zone-count-minutely"}}}'
  sleep 3
} | ./pduck.exe mcp -token "$TOKEN" | python -c "
import json,sys
for line in sys.stdin:
    line=line.strip()
    if line.startswith('{'):
        m=json.loads(line)
        if m.get('id')==3:
            d=json.loads(m['result']['content'][0]['text'])
            items=[r for r in d['items'] if r['pipeline']=='zone-count-minutely']
            assert items and items[0]['checksum'], d
            print('PASS get_schedule_results 真数据: status', items[0]['status'], '| checksum', items[0]['checksum'][:12])
"

# ── 5. pduck run 手动执行（独立进程，与 worker 共用执行器）
RUN_BEFORE=$(echo "$HIST" | python -c "import json,sys;print(json.load(sys.stdin)['totalItems'])")
./pduck.exe run -name zone-count-minutely -token "$TOKEN"
RUN_AFTER=$(curl -s "$SERVE_URL/api/collections/pipeline_runs/records?filter=%28pipeline%3D%27zone-count-minutely%27%29" -H "Authorization: $TOKEN" | python -c "import json,sys;print(json.load(sys.stdin)['totalItems'])")
[ "$RUN_AFTER" -gt "$RUN_BEFORE" ] && echo "PASS pduck run 手动执行写入历史（$RUN_BEFORE → $RUN_AFTER）"

# ── 6. 重启 serve → 调度恢复（重启后出现新的自动执行记录）
BEFORE_COUNT=$(curl -s "$SERVE_URL/api/collections/pipeline_runs/records" -H "Authorization: $TOKEN" | python -c "import json,sys;print(json.load(sys.stdin)['totalItems'])")
kill ${SERVE_PID:-} 2>/dev/null || true
sleep 1
start_serve
echo "serve 已重启，等待下一轮自动执行（最多 100s）..."
for i in $(seq 1 100); do
  NOW_COUNT=$(curl -s "$SERVE_URL/api/collections/pipeline_runs/records" -H "Authorization: $TOKEN" | python -c "import json,sys;print(json.load(sys.stdin)['totalItems'])")
  [ "$NOW_COUNT" -gt "$BEFORE_COUNT" ] && break
  sleep 1
  [ "$i" = "100" ] && { echo "FAIL: 重启后调度未恢复"; exit 1; }
done
echo "PASS 重启后调度恢复（历史 $BEFORE_COUNT → $NOW_COUNT）"

echo "票11 e2e 全部过线"
