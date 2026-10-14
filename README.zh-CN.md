<div align="center">

<!-- 顺带把 logo 放到 docs/assets/logo.png -->

# PocketDuck

**一个二进制：PocketBase 后端 + DuckDB 分析引擎**

[English](README.md)

</div>

> 基于构建 [PocketBase](https://pocketbase.io) 与 [DuckDB](https://duckdb.org)。
> 非官方社区项目，与两者均无隶属或背书关系。

PocketDuck 是面向小型分析应用的单二进制后端：

- **PocketBase** — 认证、集合、文件、实时订阅、admin UI、静态前端（`pb_public/`）
- **DuckDB** — 对本地/远端 Parquet、CSV、SQLite 文件的只读 SQL 分析，支持跨源 JOIN

只有一个 `POST /api/analyze` 接口。零驱动安装：Parquet/CSV 内置，SQLite 走 `sqlite_scan()`。

```sql
-- 一条 SQL：300 万行 Parquet 事实表 JOIN SQLite 维表
SELECT z.Borough, extract(hour FROM t.tpep_pickup_datetime) AS pickup_hour, avg(t.fare_amount)
FROM read_parquet('data/yellow_tripdata_2025-01.parquet') t
JOIN sqlite_scan('testdata/zones.sqlite', 'zones') z ON t.PULocationID = z.LocationID
GROUP BY 1, 2
```

## 快速开始

**前置**：[Go 1.22+](https://go.dev/dl/) 和一个 C 编译器（DuckDB 走 cgo 链接）：

- **Windows**：[WinLibs UCRT](https://github.com/brechtsanders/winlibs_mingw/releases)（必须 UCRT 运行时版本；MSVCRT 系工具链链接 DuckDB 会失败）
- **Linux**：`apt install gcc`（或等价命令）

```bash
git clone <repo-url> PocketDuck && cd PocketDuck
set CGO_ENABLED=1        # Windows (cmd)；Linux/macOS: export CGO_ENABLED=1
go build -o pduck.exe .  # Linux: go build -o pduck .

# 启动服务
./pduck.exe serve --http=127.0.0.1:8090
```

创建管理员（另开终端）：

```bash
./pduck.exe superuser upsert admin@example.com yourpassword
```

拿 token：

```bash
curl -s http://127.0.0.1:8090/api/collections/_superusers/auth-with-password \
  -H "Content-Type: application/json" \
  -d '{"identity":"admin@example.com","password":"yourpassword"}'
# → {"token":"...","record":{...}}
```

下载演示数据（NYC 出租车 2025-01，约 59 MB），跑跨源查询：

```bash
mkdir -p data && curl -L -o data/yellow_tripdata_2025-01.parquet \
  https://d37ci6vzurychx.cloudfront.net/trip-data/yellow_tripdata_2025-01.parquet
```

```bash
curl -s http://127.0.0.1:8090/api/analyze \
  -H "Authorization: <TOKEN>" -H "Content-Type: application/json" \
  -d '{"sql":"SELECT z.Borough, CAST(extract(hour FROM t.tpep_pickup_datetime) AS INT) AS pickup_hour, round(avg(t.fare_amount),2) AS avg_fare FROM read_parquet('"'"'data/yellow_tripdata_2025-01.parquet'"'"') t JOIN sqlite_scan('"'"'testdata/zones.sqlite'"'"','"'"'zones'"'"') z ON t.PULocationID = z.LocationID WHERE z.Borough != '"'"'Unknown'"'"' GROUP BY 1,2 ORDER BY 1,2","timeout_ms":180000}'
```

响应：

```json
{"columns":["Borough","pickup_hour","avg_fare"],"rows":[["Bronx",0,19.32],["Bronx",1,10.21],...],"row_count":166,"elapsed_ms":124,"truncated":false}
```

## 接口

`POST /api/analyze` — 需 PocketBase token 鉴权，只读，硬约束：

| 字段 | 位置 | 说明 |
|---|---|---|
| `sql` | body | 必填；只允许查询——DuckDB 以 `access_mode=read_only` 打开，写语句引擎层直接拒绝 |
| `params` | body | 可选的位置参数 |
| `timeout_ms` | body | 默认 30000，上限 300000；超时在服务端中断查询 |

响应：`{columns, rows, row_count, elapsed_ms, truncated}`。行数上限 10,000，超出截断并标记。

验证是可证伪的：演示查询的期望结果由独立的 DuckDB CLI 固化（[`testdata/expected/`](testdata/expected/)），逐行比对——166 行完全一致，笔记本上 P95 ≈ 140ms。

## 路线图

| 计划 | 状态 |
|---|---|
| 只读 `/api/analyze`（Parquet/CSV/SQLite） | ✅ v0 |
| 写侧流水线（SQL 文件 → cron → Parquet/PG） | 规划中 |
| PostgreSQL / MySQL 数据源 | 规划中 |
| SQL 控制台页、xlsx 导出 | 远期 |

## 许可

MIT。PocketBase 与 DuckDB 是独立的 MIT 项目；其名称仅用于描述本项目的构建基础。
