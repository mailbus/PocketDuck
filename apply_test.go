package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 票10: apply 校验 + 幂等 upsert + view 引用检查（stub serve）。

func applyStub(t *testing.T, records map[string]func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *mcpClient) {
	t.Helper()
	mux := http.NewServeMux()
	for path, h := range records {
		mux.HandleFunc(path, h)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &mcpClient{serveURL: srv.URL, token: "tok", http: srv.Client()}
}

func TestApplyValidate(t *testing.T) {
	// 合法 named query
	artifact, err := parseArtifact([]byte("kind: named_query\nname: demo\nsql: SELECT 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.kind != "named_query" || artifact.collection != "saved_queries" || artifact.name != "demo" {
		t.Fatalf("解析不对: %+v", artifact)
	}
	if errs := artifact.validate(); len(errs) != 0 {
		t.Fatalf("合法文档报错: %v", errs)
	}

	// 缺 sql → 结构化错误，路径明确
	artifact, err = parseArtifact([]byte("kind: named_query\nname: demo\n"))
	if err != nil {
		t.Fatal(err)
	}
	errs := artifact.validate()
	if len(errs) == 0 || !strings.Contains(errs[0].Message, "sql") {
		t.Fatalf("期望 sql 缺失错误, got %v", errs)
	}

	// 未知 kind / chart 非法图型
	if _, err := parseArtifact([]byte("kind: magic\n")); err == nil {
		t.Fatal("未知 kind 应报错")
	}
	artifact, _ = parseArtifact([]byte("kind: view\nname: v\nquery: q\nchart: {type: pie}\n"))
	if errs := artifact.validate(); len(errs) == 0 {
		t.Fatal("chart.type: pie 应被 schema 拒绝")
	}
}

func TestApplyUpsertIdempotent(t *testing.T) {
	methods := []string{}
	srv, c := applyStub(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"/api/collections/saved_queries/records": func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				// 第一次空，第二次命中（幂等路径走 PATCH）
				if len(methods) >= 1 {
					w.Write([]byte(`{"items":[{"id":"rec1","name":"demo"}],"totalItems":1}`))
				} else {
					w.Write([]byte(`{"items":[],"totalItems":0}`))
				}
				return
			}
			methods = append(methods, r.Method)
			w.Write([]byte(`{"id":"rec1"}`))
		},
		"/api/collections/saved_queries/records/rec1": func(w http.ResponseWriter, r *http.Request) {
			methods = append(methods, "PATCH")
			w.Write([]byte(`{"id":"rec1"}`))
		},
	})
	_ = srv

	artifact, _ := parseArtifact([]byte("kind: named_query\nname: demo\nsql: SELECT 1\n"))
	action, err := applyOne(c, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if action != "created" {
		t.Fatalf("首次 apply 应 created, got %s", action)
	}
	action, err = applyOne(c, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if action != "updated" {
		t.Fatalf("重复 apply 应 updated, got %s", action)
	}
	if strings.Join(methods, ",") != "POST,PATCH" {
		t.Fatalf("期望 POST 后 PATCH, got %v", methods)
	}
}

func TestApplyViewRefCheck(t *testing.T) {
	srv, c := applyStub(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"/api/collections/saved_queries/records": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"items":[],"totalItems":0}`))
		},
	})
	_ = srv
	artifact, _ := parseArtifact([]byte("kind: view\nname: v\nquery: missing_q\nchart: {type: bar, x: a, y: b}\n"))
	if errs := artifact.validate(); len(errs) != 0 {
		t.Fatalf("schema 层不应报错: %v", errs)
	}
	err := c.applySemantic(context.Background(), artifact)
	if err == nil || !strings.Contains(err.Error(), "missing_q") {
		t.Fatalf("view 引用不存在的 named query 应报错, got %v", err)
	}
}

// get_view 真数据: view 记录 → 引用的 named query 执行 → {view, data}
func TestGetViewRealData(t *testing.T) {
	srv, c := applyStub(t, map[string]func(w http.ResponseWriter, r *http.Request){
		"/api/collections/views/records": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"items":[{"id":"v1","name":"fare","query":"demo","chart":{"type":"bar","x":"Borough","y":"total_fare","title":"t"}}],"totalItems":1}`))
		},
		"/api/collections/saved_queries/records": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"items":[{"id":"q1","name":"demo","sql":"SELECT Borough, SUM(fare_amount) AS total_fare FROM t GROUP BY 1"}],"totalItems":1}`))
		},
		"/api/analyze": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"columns":["Borough","total_fare"],"rows":[["Bronx",100.5]],"row_count":1,"elapsed_ms":2,"truncated":false}`))
		},
	})
	_ = srv
	res, _, err := c.getView(context.Background(), nil, nameArgs{Name: "fare"})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal([]byte(resultText(res)), &out)
	if out["data"] == nil {
		t.Fatalf("get_view 应含 data: %v", out)
	}
	view := out["view"].(map[string]any)
	if view["chart"].(map[string]any)["type"] != "bar" {
		t.Fatalf("chart spec 丢失: %v", view)
	}
}
