"""清空 PB collections 的全部记录（测试脚本用；用法: pb_clean.py <serve_url> <token>）"""
import json
import sys
import urllib.request

serve, token = sys.argv[1], sys.argv[2]

def api(method, path):
    req = urllib.request.Request(serve + path, method=method)
    req.add_header("Authorization", token)
    with urllib.request.urlopen(req) as r:
        return json.load(r) if r.status != 204 else {}

for col in sys.argv[3:]:
    try:
        recs = api("GET", f"/api/collections/{col}/records?perPage=200")
    except Exception as e:
        print(f"skip {col}: {e}", file=sys.stderr)
        continue
    for item in recs.get("items", []):
        api("DELETE", f"/api/collections/{col}/records/{item['id']}")
print("cleaned:", ", ".join(sys.argv[3:]))
