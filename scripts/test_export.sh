#!/usr/bin/env bash
# 票05: 写侧流水线测试——Docker 起真 PG，跑 Go 集成测试
set -euo pipefail
cd "$(dirname "$0")/.."

# Windows 本机需指定能编 duckdb 的工具链；其他平台用默认
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    export CGO_ENABLED=1 CC="D:/tools/mingw64/bin/gcc.exe" CXX="D:/tools/mingw64/bin/g++.exe" ;;
  *) export CGO_ENABLED=1 ;;
esac

PORT=${PDUCK_TEST_PG_PORT:-55432}
export PDUCK_TEST_PG_DSN="postgres://pduck:pduck@localhost:$PORT/pduck_test?sslmode=disable"

docker rm -f pduck-test-pg >/dev/null 2>&1 || true
docker run -d --name pduck-test-pg \
  -e POSTGRES_USER=pduck -e POSTGRES_PASSWORD=pduck -e POSTGRES_DB=pduck_test \
  -p $PORT:5432 postgres:16-alpine >/dev/null
trap 'docker rm -f pduck-test-pg >/dev/null 2>&1 || true' EXIT

for i in $(seq 1 30); do
  docker exec pduck-test-pg pg_isready -U pduck >/dev/null 2>&1 && break
  sleep 1
done

exec go test -v -run 'TestExport' .
