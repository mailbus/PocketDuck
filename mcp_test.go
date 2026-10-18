package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 票09: MCP 读面的 serve 客户端逻辑——用 httptest 桩 serve 验证四工具行为。

func mcpStubServe(t *testing.T, analyze func(w http.ResponseWriter, r *http.Request), records map[string]func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *mcpClient) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/analyze", analyze)
	for path, h := range records {
		mux.HandleFunc(path, h)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &mcpClient{serveURL: srv.URL, token: "testtoken", http: srv.Client()}
}

func TestMCPClientRunSQL(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	srv, c := mcpStubServe(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"columns":["n"],"rows":[[1]],"row_count":1,"elapsed_ms":3,"truncated":false}`))
	}, nil)

	res, _, err := c.runSQL(context.Background(), nil, runSQLArgs{SQL: "SELECT 1 AS n"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "testtoken" {
		t.Errorf("token 未透传: %q", gotAuth)
	}
	if gotBody["sql"] != "SELECT 1 AS n" {
		t.Errorf("sql 未透传: %v", gotBody)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
		t.Fatal(err)
	}
	if out["row_count"] != float64(1) {
		t.Errorf("结果不对: %v", out)
	}
	_ = srv
}

func TestMCPClientNamedQuery(t *testing.T) {
	var ranSQL string
	srv, c := mcpStubServe(t, func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		ranSQL, _ = b["sql"].(string)
		w.Write([]byte(`{"columns":["n"],"rows":[[42]],"row_count":1,"elapsed_ms":1,"truncated":false}`))
	}, map[string]func(w http.ResponseWriter, r *http.Request){
		"/api/collections/saved_queries/records": func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.URL.Query().Get("filter"), `name='demo'`) {
				t.Errorf("filter 缺 name: %s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"items":[{"name":"demo","sql":"SELECT 42 AS n","params":null}],"totalItems":1}`))
		},
	})

	res, _, err := c.runNamedQuery(context.Background(), nil, nameArgs{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if ranSQL != "SELECT 42 AS n" {
		t.Errorf("执行的不是固化 SQL: %q", ranSQL)
	}
	if !strings.Contains(resultText(res), "42") {
		t.Errorf("结果缺值: %s", resultText(res))
	}
	_ = srv
}

func TestMCPClientNamedQueryNotFound(t *testing.T) {
	srv, c := mcpStubServe(t, func(w http.ResponseWriter, r *http.Request) {}, map[string]func(w http.ResponseWriter, r *http.Request){
		"/api/collections/saved_queries/records": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"items":[],"totalItems":0}`))
		},
	})
	if _, _, err := c.runNamedQuery(context.Background(), nil, nameArgs{Name: "nope"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("期望 not found 错误, got %v", err)
	}
	_ = srv
}

// 空态语义: collection 未建（票11/10 才有）→ 结构化空态而非报错
func TestMCPClientEmptyStates(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := &mcpClient{serveURL: srv.URL, token: "t", http: srv.Client()}

	res, _, err := c.getScheduleResults(context.Background(), nil, nameArgs{Name: "weekly"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(res), `"results":[]`) {
		t.Errorf("schedule 空态不对: %s", resultText(res))
	}
	res, _, err = c.getView(context.Background(), nil, nameArgs{Name: "fare"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resultText(res), `"view":null`) {
		t.Errorf("view 空态不对: %s", resultText(res))
	}
}
