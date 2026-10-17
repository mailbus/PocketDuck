#!/usr/bin/env bash
# 票04 验收：模拟陌生人 clone → README → 跑通示例查询，全程计时（冷缓存）
set -eu
FRESH=/d/tmp-pduck-fresh
rm -rf "$FRESH"; mkdir -p "$FRESH"; cd "$FRESH"

t0=$(date +%s)
lap() { echo "[$(( $(date +%s) - t0 ))s] $1"; }

lap "clone start"
git clone -q http://192.168.50.53:43929/yards/PocketDuck.git PocketDuck
cd PocketDuck
lap "clone done"

export CGO_ENABLED=1 CC="D:\\tools\\mingw64\\bin\\gcc.exe" \
  GOMODCACHE="D:\\tmp-pduck-fresh\\gomodcache" GOCACHE="D:\\tmp-pduck-fresh\\gocache" \
  GOPROXY=https://goproxy.cn,direct

go build -o pduck.exe .
lap "cold build done (fresh module+build caches)"

./pduck.exe superuser upsert admin@example.com yourpassword
./pduck.exe serve --http=127.0.0.1:8091 &
SERVE_PID=$!
sleep 3
lap "serve up"

TOKEN=$(curl -s http://127.0.0.1:8091/api/collections/_superusers/auth-with-password \
  -H "Content-Type: application/json" \
  -d '{"identity":"admin@example.com","password":"yourpassword"}' \
  | python -c "import sys,json;print(json.load(sys.stdin)['token'])")
lap "token obtained"

curl -sL -o data/x.parquet --create-dirs \
  https://d37ci6vzurychx.cloudfront.net/trip-data/yellow_tripdata_2025-01.parquet
mkdir -p data && mv data/x.parquet data/yellow_tripdata_2025-01.parquet 2>/dev/null || true
lap "demo data downloaded"

python - "$TOKEN" <<'EOF'
import json, sys, urllib.request
sql = open('testdata/gt_borough_hour_fare.sql', encoding='utf-8').read()
req = urllib.request.Request('http://127.0.0.1:8091/api/analyze',
    data=json.dumps({'sql': sql, 'timeout_ms': 180000}).encode(),
    headers={'Authorization': sys.argv[1], 'Content-Type': 'application/json'})
d = json.load(urllib.request.urlopen(req))
assert d['row_count'] == 166 and not d['truncated'], d
print(f"example query OK: {d['row_count']} rows in {d['elapsed_ms']}ms")
EOF
lap "example query OK"

kill $SERVE_PID 2>/dev/null || true
TOTAL=$(( $(date +%s) - t0 ))
echo "TOTAL: ${TOTAL}s ($(( TOTAL / 60 ))min $(( TOTAL % 60 ))s) — 通过线 15min=900s"
[ "$TOTAL" -lt 900 ] && echo PASS || echo FAIL
