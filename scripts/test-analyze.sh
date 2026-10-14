#!/usr/bin/env bash
# 票02 验收脚本：/api/analyze 契约与约束
# 用法: 先启动 ./pduck.exe serve --http=127.0.0.1:8090 再跑本脚本
set -u
BASE="http://127.0.0.1:8090"
PASS=0; FAIL=0
ok()   { PASS=$((PASS+1)); echo "  ✓ $1"; }
bad()  { FAIL=$((FAIL+1)); echo "  ✗ $1"; }

TOKEN=$(curl -s "$BASE/api/collections/_superusers/auth-with-password" \
  -H "Content-Type: application/json" \
  -d '{"identity":"test@pduck.local","password":"pass12345"}' | python -c "import sys,json;print(json.load(sys.stdin).get('token',''))")
[ -n "$TOKEN" ] && ok "superuser token obtained" || bad "superuser auth failed"

H="Authorization: $TOKEN"

echo "[1] 无 token → 401"
code=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/api/analyze" -H "Content-Type: application/json" -d '{"sql":"SELECT 1"}')
[ "$code" = "401" ] && ok "401" || bad "got $code"

echo "[2] 带 token SELECT 1 → 契约字段齐全"
r=$(curl -s -X POST "$BASE/api/analyze" -H "$H" -H "Content-Type: application/json" -d '{"sql":"SELECT 1 AS one"}')
echo "$r" | python -c "
import sys,json; d=json.load(sys.stdin)
assert d['columns']==['one'] and d['rows']==[[1]] and d['row_count']==1
assert 'elapsed_ms' in d and d['truncated']==False
" 2>/dev/null && ok "contract ok" || bad "contract: $r"

echo "[3] 慢查询被 timeout 掐断"
r=$(curl -s -X POST "$BASE/api/analyze" -H "$H" -H "Content-Type: application/json" \
  -d '{"sql":"SELECT count(*) FROM range(10000000000)","timeout_ms":800}')
echo "$r" | grep -qi "query failed" && ok "timeout enforced" || bad "no timeout: $r"

echo "[4] >10k 行截断 truncated=true"
r=$(curl -s -X POST "$BASE/api/analyze" -H "$H" -H "Content-Type: application/json" \
  -d '{"sql":"SELECT i FROM range(20000) t(i)"}')
echo "$r" | python -c "
import sys,json; d=json.load(sys.stdin)
assert d['row_count']==10000 and d['truncated']==True
" 2>/dev/null && ok "truncated at 10000" || bad "$r"

echo "[5] 写 SQL 被只读连接拒绝"
r=$(curl -s -X POST "$BASE/api/analyze" -H "$H" -H "Content-Type: application/json" \
  -d '{"sql":"CREATE TABLE t(x INT)"}')
echo "$r" | grep -qi "query failed" && ok "read-only enforced" || bad "write allowed: $r"

echo "[6] 分析查询期间 PB 普通 API 不受阻塞"
curl -s -X POST "$BASE/api/analyze" -H "$H" -H "Content-Type: application/json" \
  -d '{"sql":"SELECT count(*) FROM range(20000000000)","timeout_ms":8000}' > /dev/null &
BG=$!
sleep 1
code=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/api/health")
kill $BG 2>/dev/null; wait $BG 2>/dev/null
[ "$code" = "200" ] && ok "/api/health still 200 during long analysis" || bad "health $code during analysis"

echo
echo "RESULT: $PASS passed, $FAIL failed"
exit $FAIL
