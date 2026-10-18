# AGENTS.md — Using PocketDuck from AI agents

PocketDuck is a single-binary backend: PocketBase (auth/files/realtime) + DuckDB (analytics). This guide is for AI agents (Claude Code, Codex, OpenCode, …) that want to query analytics, author artifacts, or operate pipelines against a running PocketDuck instance.

## 0. Prerequisites

- A running `pduck serve` (default `http://127.0.0.1:8090`)
- A PocketBase superuser token (`PDUCK_TOKEN`); get one:
  ```bash
  curl -s http://127.0.0.1:8090/api/collections/_superusers/auth-with-password \
    -H "Content-Type: application/json" \
    -d '{"identity":"admin@example.com","password":"..."}'
  ```

## 1. Connect as MCP (read tools)

**Claude Code** (`.mcp.json` in project root):
```json
{"mcpServers":{"pduck":{"command":"/path/to/pduck","args":["mcp"],"env":{"PDUCK_TOKEN":"<token>"}}}}
```

**Codex CLI** (`~/.codex/config.toml`):
```toml
[mcp_servers.pduck]
command = "/path/to/pduck"
args = ["mcp"]
env = { "PDUCK_TOKEN" = "<token>" }
```

**OpenCode** (`opencode.json`):
```json
{"mcp":{"pduck":{"type":"local","command":["/path/to/pduck","mcp"],"environment":{"PDUCK_TOKEN":"<token>"}}}}
```

(Flag names vary by version — check each tool's current MCP docs.)

## 2. Tool selection

| Tool | Use when |
|------|----------|
| `run_sql(sql, params?, timeout_ms?)` | ad-hoc exploration; read-only SELECT only, 10k row cap |
| `run_named_query(name, params?)` | a curated query already exists — prefer this over duplicating SQL |
| `get_schedule_results(name?)` | you need outputs of scheduled pipelines (rows/checksum/snapshot paths) |
| `get_view(name?)` | you need a saved analysis view as `{view: {chart spec}, data}` — same view humans see |

Result shape for queries: `{columns, rows, row_count, elapsed_ms, truncated}`.

## 3. Authoring artifacts (`pduck apply`)

Write a YAML draft, then `PDUCK_TOKEN=<token> pduck apply -f draft.yaml`. Apply is idempotent (same name → update). Validation errors come back as structured JSON `{"ok":false,"errors":[{"path","message"}]}` with exit code 2 — **read them, fix the YAML, re-apply**.

```yaml
# named query — reusable SQL others (humans, agents, pipelines) will run by name
kind: named_query
name: borough-fare
sql: SELECT z.Borough, SUM(t.fare_amount) AS total_fare FROM read_parquet('data/x.parquet') t JOIN sqlite_scan('testdata/zones.sqlite','zones') z ON t.PULocationID = z.LocationID GROUP BY 1
```

```yaml
# view — a chart over a named query (types: bar | line | table)
kind: view
name: borough-fare-bar
query: borough-fare
chart: {type: bar, x: Borough, y: total_fare, title: Fare by borough}
```

```yaml
# pipeline — declarative task list; schedule is standard 5-field cron (omit = manual)
kind: pipeline
name: borough-fare-weekly
schedule: "0 8 * * 1"
tasks:
  - query: borough-fare      # named-query ref (or raw SQL)
  - export: to=out.xlsx      # parquet/xlsx/postgres — reuses the last query's SQL
  - push: channel=feishu     # webhook | feishu | slack (URLs come from env, never put them in YAML)
```

## 4. Guardrails

- `run_sql` is read-only by design — writes are rejected at the engine level. To materialize data, author a pipeline (snapshot/export) instead of trying INSERT.
- Pipeline snapshots overwrite by name; the checksum in run history and push messages lets you verify content.
- Webhook URLs (`PDUCK_FEISHU_WEBHOOK`, `PDUCK_SLACK_WEBHOOK`, `PDUCK_WEBHOOK`) are credentials — keep them in env, never in YAML or git.
- Verify connectivity first: `run_sql` with `SELECT 1`.
