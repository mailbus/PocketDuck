package main

// mcp.go: 票09 MCP 读面——agent 的读信息工具，代理 serve HTTP（规格 v0x grill 定案）。
// 不直连 DuckDB：固化产物（named query / 运行历史 / 视图）活在 serve 里，
// 代理最薄且不分裂存储。写面（固化）走 pduck apply，是另一条路径（票10）。

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpClient struct {
	serveURL string
	token    string
	http     *http.Client
}

// getJSON: PB 记录 API 通用 GET；token 鉴权同 /api/analyze（裸 token）。
func (c *mcpClient) getJSON(ctx context.Context, path string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.serveURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("serve 不可达（pduck serve 是否在跑？）: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("serve 响应非 JSON (HTTP %d): %s", resp.StatusCode, body)
	}
	out["_status"] = float64(resp.StatusCode)
	return out, nil
}

// postAnalyze: POST /api/analyze，返回原始 JSON 文本（agent 直接可读）。
func (c *mcpClient) postAnalyze(ctx context.Context, req analyzeRequest) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serveURL+"/api/analyze", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	hreq.Header.Set("Authorization", c.token)
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(hreq)
	if err != nil {
		return "", fmt.Errorf("serve 不可达（pduck serve 是否在跑？）: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("analyze HTTP %d: %s", resp.StatusCode, raw)
	}
	return string(raw), nil
}

// runSQLArgs: 即席探索查询。
type runSQLArgs struct {
	SQL       string `json:"sql" jsonschema:"the SELECT query to run"`
	Params    []any  `json:"params,omitempty" jsonschema:"optional positional SQL parameters"`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"query timeout in ms, default 30000, max 300000"`
}

func (c *mcpClient) runSQL(ctx context.Context, req *mcp.CallToolRequest, a runSQLArgs) (*mcp.CallToolResult, any, error) {
	if a.SQL == "" {
		return nil, nil, fmt.Errorf("sql is required")
	}
	out, err := c.postAnalyze(ctx, analyzeRequest{SQL: a.SQL, Params: a.Params, TimeoutMS: a.TimeoutMS})
	if err != nil {
		return nil, nil, err
	}
	return textResult(out), nil, nil
}

// nameArgs: 按名字取固化产物的工具入参。
type nameArgs struct {
	Name string `json:"name,omitempty" jsonschema:"the name of the saved artifact; optional for list-style calls"`
}

func (c *mcpClient) runNamedQuery(ctx context.Context, req *mcp.CallToolRequest, a nameArgs) (*mcp.CallToolResult, any, error) {
	rec, err := c.findRecord(ctx, "saved_queries", a.Name)
	if err != nil {
		return nil, nil, err
	}
	sql, _ := rec["sql"].(string)
	if sql == "" {
		return nil, nil, fmt.Errorf("named query %q has empty sql", a.Name)
	}
	var params []any
	if raw, ok := rec["params"].(string); ok && raw != "" && raw != "null" {
		json.Unmarshal([]byte(raw), &params)
	}
	out, err := c.postAnalyze(ctx, analyzeRequest{SQL: sql, Params: params})
	if err != nil {
		return nil, nil, err
	}
	return textResult(out), nil, nil
}

// getScheduleResults: 定时管道运行历史（票11 出真数据；collection 未建时结构化空态）。
func (c *mcpClient) getScheduleResults(ctx context.Context, req *mcp.CallToolRequest, a nameArgs) (*mcp.CallToolResult, any, error) {
	empty := `{"results":[],"note":"no runs yet; scheduling lands with ticket 11"}`
	recs, err := c.getJSON(ctx, "/api/collections/pipeline_runs/records?perPage=20&sort=-created")
	if err != nil || recs["_status"] != float64(http.StatusOK) {
		return textResult(empty), nil, nil // collection 未建（404）等 → 空态
	}
	if a.Name != "" {
		filter := url.QueryEscape(fmt.Sprintf("(pipeline='%s')", a.Name))
		recs, err = c.getJSON(ctx, "/api/collections/pipeline_runs/records?perPage=20&sort=-created&filter="+filter)
		if err != nil {
			return textResult(empty), nil, nil
		}
	}
	delete(recs, "_status")
	raw, _ := json.Marshal(recs)
	return textResult(string(raw)), nil, nil
}

// getView: UI-as-JSON（票10）——指定 name 返回 {view, data}（跑引用的 named query），
// 不带 name 返回视图清单；views collection 未建时空态。
func (c *mcpClient) getView(ctx context.Context, req *mcp.CallToolRequest, a nameArgs) (*mcp.CallToolResult, any, error) {
	empty := `{"view":null,"note":"no views yet; apply one with pduck apply"}`
	recs, err := c.getJSON(ctx, "/api/collections/views/records?perPage=20")
	if err != nil || recs["_status"] != float64(http.StatusOK) {
		return textResult(empty), nil, nil
	}
	items, _ := recs["items"].([]any)
	if len(items) == 0 {
		return textResult(empty), nil, nil
	}
	if a.Name == "" {
		raw, _ := json.Marshal(items)
		return textResult(string(raw)), nil, nil
	}
	for _, it := range items {
		rec, _ := it.(map[string]any)
		if name, _ := rec["name"].(string); name == a.Name {
			queryRef, _ := rec["query"].(string)
			data, _, err := c.runNamedQuery(ctx, req, nameArgs{Name: queryRef})
			if err != nil {
				return nil, nil, fmt.Errorf("view %q 的 named query %q 执行失败: %w", a.Name, queryRef, err)
			}
			out, _ := json.Marshal(map[string]any{"view": rec, "data": json.RawMessage(resultText(data))})
			return textResult(string(out)), nil, nil
		}
	}
	return nil, nil, fmt.Errorf("view %q not found", a.Name)
}

// findRecord: 按 name 查 PB collection 记录；未命中返回 not found 错误。
func (c *mcpClient) findRecord(ctx context.Context, collection, name string) (map[string]any, error) {
	filter := url.QueryEscape(fmt.Sprintf("(name='%s')", strings.ReplaceAll(name, "'", "''")))
	recs, err := c.getJSON(ctx, fmt.Sprintf("/api/collections/%s/records?filter=%s", collection, filter))
	if err != nil {
		return nil, err
	}
	status, _ := recs["_status"].(float64)
	if status != http.StatusOK {
		return nil, fmt.Errorf("collection %q unavailable (HTTP %.0f)", collection, status)
	}
	items, _ := recs["items"].([]any)
	if len(items) == 0 {
		return nil, fmt.Errorf("%s %q not found", strings.TrimSuffix(collection, "s"), name)
	}
	first, _ := items[0].(map[string]any)
	return first, nil
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// mcpCmd: 用法 pduck mcp -serve http://127.0.0.1:8090 -token <PB token>
// token 也可走环境变量 PDUCK_TOKEN；serve 地址默认 127.0.0.1:8090（PDUCK_SERVE 覆盖）。
func mcpCmd() {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	serve := fs.String("serve", "", "pduck serve 地址（默认 http://127.0.0.1:8090）")
	token := fs.String("token", "", "PB token（默认读 PDUCK_TOKEN）")
	fs.Parse(os.Args[2:])

	cfg := &mcpClient{
		serveURL: *serve,
		token:    *token,
		http:     &http.Client{},
	}
	if cfg.serveURL == "" {
		cfg.serveURL = os.Getenv("PDUCK_SERVE")
	}
	if cfg.serveURL == "" {
		cfg.serveURL = "http://127.0.0.1:8090"
	}
	if cfg.token == "" {
		cfg.token = os.Getenv("PDUCK_TOKEN")
	}
	if cfg.token == "" {
		log.Fatal("mcp: 需要 PB token（-token 或环境变量 PDUCK_TOKEN）")
	}
	cfg.serveURL = strings.TrimRight(cfg.serveURL, "/")

	server := mcp.NewServer(&mcp.Implementation{Name: "pduck", Version: "0.1.0"}, &mcp.ServerOptions{
		Instructions: "PocketDuck analytics read tools. run_sql for ad-hoc exploration, run_named_query to execute saved queries, get_schedule_results for scheduled pipeline runs, get_view for saved analysis views (JSON).",
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "run_sql",
		Description: "Run a read-only analytics SQL against the DuckDB warehouse (Parquet/CSV/SQLite/PG/MySQL sources). Returns {columns, rows, row_count, elapsed_ms, truncated}.",
	}, cfg.runSQL)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "run_named_query",
		Description: "Execute a named query saved on the pduck serve (saved_queries collection).",
	}, cfg.runNamedQuery)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_schedule_results",
		Description: "List recent scheduled pipeline runs (with results/snapshot refs). Name optional to filter by pipeline.",
	}, cfg.getScheduleResults)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_view",
		Description: "Get a saved analysis view as JSON (chart spec + data). Name optional to list all.",
	}, cfg.getView)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Printf("mcp server exited: %v", err)
	}
}

// resultText: 取 CallToolResult 的首个文本内容（测试与调用侧共用）。
func resultText(r *mcp.CallToolResult) string {
	if r == nil {
		return ""
	}
	for _, c := range r.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			return t.Text
		}
	}
	return ""
}
