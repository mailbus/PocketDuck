<div align="center">

<img src="docs/assets/logo.png" alt="PocketDuck" width="200"/>

# PocketDuck

**一个二进制：PocketBase 后端 + DuckDB 分析引擎**

[English](README.md) · [llms.txt](llms.txt)

</div>

> 基于构建 [PocketBase](https://pocketbase.io) 与 [DuckDB](https://duckdb.org)。
> 非官方社区项目，与两者均无隶属或背书关系。

PocketDuck 是面向小型分析应用的单二进制后端：

- **PocketBase** — 认证、集合、文件、实时订阅、admin UI、静态前端（`pb_public/`）
- **DuckDB** — 对本地/远端 Parquet、CSV、SQLite 文件的 SQL 分析，支持跨源 JOIN

多源进出：

- **进**：`pduck import` 把 PostgreSQL/MySQL 表拉进 DuckDB 仓库（10 万行 ≈ 4 秒）
- **出**：`pduck export` 把查询结果写到 Parquet、PostgreSQL 或 Excel（`out.xlsx`）
- **浏览**：内置 SQL 控制台页（PocketBase 账号登录、跑查询、看结果）

分析查询永不进 PocketBase 请求热路径：`POST /api/analyze` 是独立路由，带超时和行数上限。零驱动安装：Parquet/CSV 内置，SQLite 走 `sqlite_scan()`。

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
git clone https://github.com/mailbus/PocketDuck && cd PocketDuck
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

## 给 AI Agent

PocketDuck 原生说 MCP：`pduck mcp` 向 Claude Code、Codex、OpenCode 等暴露读面工具（run_sql、run_named_query、get_schedule_results、get_view）；agent 用 YAML 编写查询/视图/管道，`pduck apply` 提交生效。完整指引见 [AGENTS.md](AGENTS.md)（含各家 MCP 配置片段）。

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
| 写侧流水线（SQL 文件 → cron → Parquet/PG） | ✅ v0 |
| PostgreSQL / MySQL 数据源 | ✅ v0 |
| SQL 控制台页 | ✅ v0 |
| xlsx 导出（`pduck export -to out.xlsx`） | ✅ v0 |
| MCP 读面工具（`pduck mcp`），面向 agent | ✅ v0.x |
| `pduck apply` 声明式固化（查询/视图/管道） | ✅ v0.x |
| 管道定时执行 + 数据快照 | ✅ v0.x |
| 推送到 webhook / 飞书机器人 / Slack | ✅ v0.x |

## 许可

MIT。PocketBase 与 DuckDB 是独立的 MIT 项目；其名称仅用于描述本项目的构建基础。
