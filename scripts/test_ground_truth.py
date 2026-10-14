# 票03 验收：跨源 JOIN（Parquet × SQLite）结果与 CLI 固化的标准答案逐行一致 + P95 < 5s
# 用法: 先启动 ./pduck.exe serve --http=127.0.0.1:8090，再 python scripts/test_ground_truth.py
import csv, json, time, statistics, urllib.request

BASE = "http://127.0.0.1:8090"
EXPECTED = "testdata/expected/borough_hour_fare.csv"

def auth():
    body = json.dumps({"identity": "test@pduck.local", "password": "pass12345"}).encode()
    req = urllib.request.Request(BASE + "/api/collections/_superusers/auth-with-password",
                                 data=body, headers={"Content-Type": "application/json"})
    return json.load(urllib.request.urlopen(req))["token"]

def analyze(token, sql, timeout_ms=180000):
    req = urllib.request.Request(BASE + "/api/analyze",
                                 data=json.dumps({"sql": sql, "timeout_ms": timeout_ms}).encode(),
                                 headers={"Authorization": token, "Content-Type": "application/json"})
    t0 = time.perf_counter()
    d = json.load(urllib.request.urlopen(req))
    return d, (time.perf_counter() - t0) * 1000

def norm(rows):
    return sorted((b, int(h), round(float(f), 2)) for b, h, f in rows)

sql = open("testdata/gt_borough_hour_fare.sql", encoding="utf-8").read()
token = auth()

d, _ = analyze(token, sql)  # 预热：sqlite 扩展 autoload/下载不计时
assert d["row_count"] > 0 and not d["truncated"], d.get("message", d)

times = []
for _ in range(10):
    d, ms = analyze(token, sql)
    times.append(ms)

got = norm(d["rows"])
with open(EXPECTED, encoding="utf-8") as f:
    exp = norm(list(csv.reader(f))[1:])

if got != exp:
    diff = [(a, b) for a, b in zip(got, exp) if a != b][:5]
    raise SystemExit(f"FAIL: rows differ from ground truth, first diffs: {diff} (got {len(got)}, exp {len(exp)})")

p95 = sorted(times)[int(0.95 * len(times)) - 1]
print(f"rows match ground truth: {len(got)} rows x 3 cols")
print(f"elapsed_ms runs: {[round(t) for t in times]}")
print(f"median {statistics.median(times):.0f}ms, P95 {p95:.0f}ms")
assert p95 < 5000, f"P95 {p95:.0f}ms >= 5000ms"
print("PASS")
