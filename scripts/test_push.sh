#!/usr/bin/env bash
# 票12 e2e：push 任务真实到达。
# - 通用 webhook：本地接收端自动验证 payload（无需外部依赖）
# - 飞书：需环境变量 PDUCK_FEISHU_WEBHOOK（真实到达后请人工看群里消息）
# 用法: PDUCK_FEISHU_WEBHOOK=<url> scripts/test_push.sh
set -euo pipefail
cd "$(dirname "$0")/.."
export PYTHONIOENCODING=utf-8

SERVE_URL=http://127.0.0.1:8090
TMP=$(mktemp -d)
trap 'kill ${SERVE_PID:-} 2>/dev/null || true; rm -rf "$TMP"' EXIT

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
python scripts/pb_clean.py "$SERVE_URL" "$TOKEN" saved_queries pipelines pipeline_runs > /dev/null

# ── 1. 通用 webhook 渠道：本地接收端验证 payload
python - "$TMP/wh_received.json" > /tmp/wh_server.log 2>&1 <<'EOF' &
import http.server, json, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get('Content-Length', 0)))
        open(sys.argv[1], 'wb').write(body)
        self.send_response(200); self.end_headers(); self.wfile.write(b'{"ok":true}')
    def log_message(self, *a): pass
http.server.HTTPServer(('127.0.0.1', 8901), H).serve_forever()
EOF
WH_PID=$!
sleep 1
trap 'kill ${SERVE_PID:-} ${WH_PID:-} 2>/dev/null || true; rm -rf "$TMP"' EXIT

cat > "$TMP/p_wh.yaml" <<EOF
kind: pipeline
name: push-webhook-e2e
tasks:
  - query: SELECT COUNT(*) AS trips FROM read_parquet('data/yellow_tripdata_2025-01.parquet')
  - push: channel=webhook
EOF
PDUCK_WEBHOOK=http://127.0.0.1:8901 ./pduck.exe apply -f "$TMP/p_wh.yaml" -token "$TOKEN" > /dev/null
PDUCK_WEBHOOK=http://127.0.0.1:8901 ./pduck.exe run -name push-webhook-e2e -token "$TOKEN"
sleep 1
python - "$TMP/wh_received.json" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1], encoding='utf-8'))
assert 'pduck' in d['text'] and 'push-webhook-e2e' in d['text'] and 'success' in d['text'], d
print('PASS 通用 webhook payload:', d['text'][:80])
EOF

# ── 2. 飞书真实到达（需 PDUCK_FEISHU_WEBHOOK；到达请人工确认群里消息）
if [ -n "${PDUCK_FEISHU_WEBHOOK:-}" ]; then
  cat > "$TMP/p_fs.yaml" <<'EOF'
kind: pipeline
name: push-feishu-e2e
tasks:
  - query: SELECT COUNT(*) AS trips FROM read_parquet('data/yellow_tripdata_2025-01.parquet')
  - push: channel=feishu
EOF
  ./pduck.exe apply -f "$TMP/p_fs.yaml" -token "$TOKEN" > /dev/null
  ./pduck.exe run -name push-feishu-e2e -token "$TOKEN"
  echo "PASS 飞书已发送（HTTP 200）→ 请人工确认群里到达并截图"
else
  echo "SKIP 飞书真实到达: 未设置 PDUCK_FEISHU_WEBHOOK（export 后重跑）"
fi

echo "票12 e2e 完成"
