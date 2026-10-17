#!/usr/bin/env bash
# 票06: 读侧测试——Docker 起真 PG + 真 MySQL，跑 Go 集成测试
set -euo pipefail
cd "$(dirname "$0")/.."

case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    export CGO_ENABLED=1 CC="D:/tools/mingw64/bin/gcc.exe" CXX="D:/tools/mingw64/bin/g++.exe" ;;
  *) export CGO_ENABLED=1 ;;
esac

PGPORT=${PDUCK_TEST_PG_PORT:-55432}
MYPORT=${PDUCK_TEST_MYSQL_PORT:-53306}
export PDUCK_TEST_PG_DSN="postgres://pduck:pduck@localhost:$PGPORT/pduck_test?sslmode=disable"
export PDUCK_TEST_MYSQL_DSN="mysql://pduck:pduck@localhost:$MYPORT/pduck_test"

cleanup() {
  docker rm -f pduck-test-pg pduck-test-my >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

docker run -d --name pduck-test-pg \
  -e POSTGRES_USER=pduck -e POSTGRES_PASSWORD=pduck -e POSTGRES_DB=pduck_test \
  -p $PGPORT:5432 postgres:16-alpine >/dev/null
docker run -d --name pduck-test-my \
  -e MYSQL_ROOT_PASSWORD=pduck -e MYSQL_USER=pduck -e MYSQL_PASSWORD=pduck -e MYSQL_DATABASE=pduck_test \
  -p $MYPORT:3306 mysql:8 >/dev/null

for i in $(seq 1 30); do
  docker exec pduck-test-pg pg_isready -U pduck >/dev/null 2>&1 && break
  sleep 1
done
for i in $(seq 1 60); do
  # mysqladmin ping 会误报：初始化期临时服务器 socket 可连但密码未设，须验真连接
  docker exec pduck-test-my mysql -h127.0.0.1 -uroot -ppduck -e 'SELECT 1' >/dev/null 2>&1 && break
  sleep 1
done

# 播种 MySQL 源表（与 PG 侧 rt_src 同构）
docker exec pduck-test-my mysql -uroot -ppduck pduck_test -e   "CREATE TABLE rt_src(id INT PRIMARY KEY, borough VARCHAR(50));    INSERT INTO rt_src VALUES (1,'Manhattan'),(2,'Queens'),(3,'Brooklyn');"

exec go test -v -run 'TestImport' .
