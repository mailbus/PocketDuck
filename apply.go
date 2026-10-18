package main

// apply.go: 票10 写面 CLI——本地 YAML 草稿 → JSON Schema 校验 → 结构化错误 → upsert 到 serve。
// kubectl 精神（规格 v0x grill 定案）：草稿 git 可管，apply 让"搞好了"有明确生效语义；
// 幂等：同名 upsert（存在则 PATCH，否则 POST）。

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"gopkg.in/yaml.v3"
)

// 三类 artifact 的 JSON Schema（票10 定形；agent 写错时校验错误可自修复）。
const schemaNamedQuery = `{
  "type": "object",
  "properties": {
    "kind": {"const": "named_query"},
    "name": {"type": "string", "minLength": 1},
    "sql": {"type": "string", "minLength": 1},
    "description": {"type": "string"},
    "params": {}
  },
  "required": ["kind", "name", "sql"],
  "additionalProperties": false
}`

const schemaView = `{
  "type": "object",
  "properties": {
    "kind": {"const": "view"},
    "name": {"type": "string", "minLength": 1},
    "query": {"type": "string", "minLength": 1},
    "chart": {
      "type": "object",
      "properties": {
        "type": {"enum": ["bar", "line", "table"]},
        "x": {"type": "string"},
        "y": {"type": "string"},
        "title": {"type": "string"}
      },
      "required": ["type"],
      "additionalProperties": false
    }
  },
  "required": ["kind", "name", "query", "chart"],
  "additionalProperties": false
}`

const schemaPipeline = `{
  "type": "object",
  "properties": {
    "kind": {"const": "pipeline"},
    "name": {"type": "string", "minLength": 1},
    "schedule": {"type": "string"},
    "tasks": {
      "type": "array",
      "minItems": 1,
      "items": {
        "type": "object",
        "properties": {"query": {"type": "string"}, "export": {"type": "string"}},
        "minProperties": 1,
        "additionalProperties": false
      }
    }
  },
  "required": ["kind", "name", "tasks"],
  "additionalProperties": false
}`

var artifactSchemas = map[string]string{
	"named_query": schemaNamedQuery,
	"view":        schemaView,
	"pipeline":    schemaPipeline,
}

// kind → PB collection
var artifactCollections = map[string]string{
	"named_query": "saved_queries",
	"view":        "views",
	"pipeline":    "pipelines",
}

// 各 kind 落 PB 的字段白名单（其余字段 schema 层已被 additionalProperties 拒绝）
var artifactFields = map[string][]string{
	"named_query": {"name", "sql", "description", "params"},
	"view":        {"name", "query", "chart"},
	"pipeline":    {"name", "schedule", "tasks"},
}

// fieldError: 结构化校验错误（agent 可读可自修复）。
type fieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type artifact struct {
	kind       string
	collection string
	name       string
	doc        map[string]any
}

// parseArtifact: YAML → kind 路由。未知/缺失 kind 直接报错。
func parseArtifact(data []byte) (*artifact, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("YAML 解析失败: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("空文档")
	}
	kind, _ := doc["kind"].(string)
	if kind == "" {
		return nil, fmt.Errorf(`缺少 "kind" 字段（named_query / view / pipeline）`)
	}
	if _, ok := artifactSchemas[kind]; !ok {
		return nil, fmt.Errorf("未知 kind %q（支持: named_query / view / pipeline）", kind)
	}
	name, _ := doc["name"].(string)
	return &artifact{kind: kind, collection: artifactCollections[kind], name: name, doc: doc}, nil
}

// validate: JSON Schema 校验 + name 语义检查。
func (a *artifact) validate() []fieldError {
	var errs []fieldError
	if a.name == "" {
		errs = append(errs, fieldError{Path: "name", Message: "required"})
	}
	var sch jsonschema.Schema
	if err := json.Unmarshal([]byte(artifactSchemas[a.kind]), &sch); err != nil {
		return append(errs, fieldError{Path: "schema", Message: err.Error()})
	}
	resolved, err := sch.Resolve(nil)
	if err != nil {
		return append(errs, fieldError{Path: "schema", Message: err.Error()})
	}
	if err := resolved.Validate(a.doc); err != nil {
		errs = append(errs, fieldError{Path: a.kind, Message: err.Error()})
	}
	return errs
}

// applySemantic: schema 层管不了的服务端引用检查（view.query 必须指向已存在的 named query）。
func (c *mcpClient) applySemantic(ctx context.Context, a *artifact) error {
	if a.kind == "view" {
		ref, _ := a.doc["query"].(string)
		if _, err := c.findRecord(ctx, "saved_queries", ref); err != nil {
			return fmt.Errorf("view.query 引用的 named query %q 不存在（先 apply 它）", ref)
		}
	}
	return nil
}

// recordPayload: 按 kind 白名单抽取落 PB 的字段。
func (a *artifact) recordPayload() map[string]any {
	payload := map[string]any{}
	for _, f := range artifactFields[a.kind] {
		if v, ok := a.doc[f]; ok {
			payload[f] = v
		}
	}
	return payload
}

// applyOne: 幂等 upsert——同名记录存在则 PATCH，否则 POST。
func applyOne(c *mcpClient, a *artifact) (string, error) {
	payload := a.recordPayload()
	rec, err := c.findRecord(context.Background(), a.collection, a.name)
	if err == nil {
		id, _ := rec["id"].(string)
		if _, err := c.sendRecord(http.MethodPatch, a.collection+"/records/"+id, payload); err != nil {
			return "", err
		}
		return "updated", nil
	}
	if !isNotFound(err) {
		return "", err
	}
	if _, err := c.sendRecord(http.MethodPost, a.collection+"/records", payload); err != nil {
		return "", err
	}
	return "created", nil
}

// isNotFound: findRecord 的"未命中"错误（collection 可用但无记录）。
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not found")
}

// sendRecord: PB 记录 API 通用写（POST 创建 / PATCH 更新）。
func (c *mcpClient) sendRecord(method, path string, payload map[string]any) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, c.serveURL+"/api/collections/"+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("serve 不可达（pduck serve 是否在跑？）: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	json.Unmarshal(raw, &out)
	if resp.StatusCode >= 300 {
		return out, fmt.Errorf("%s %s HTTP %d: %s", method, path, resp.StatusCode, raw)
	}
	return out, nil
}

// applyCmd: 用法 pduck apply -f query.yaml [-f view.yaml ...]
// 校验失败输出结构化 JSON 到 stdout、退出码 2（agent 可解析自修复）。
func applyCmd() {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	fs.Var(&multiFlag{}, "f", "YAML 草稿文件（可重复传 -f）")
	serve := fs.String("serve", "", "pduck serve 地址（默认 http://127.0.0.1:8090）")
	token := fs.String("token", "", "PB token（默认读 PDUCK_TOKEN）")
	fs.Parse(os.Args[2:])

	if len(multiFlagValue) == 0 {
		fs.Usage()
		os.Exit(2)
	}

	cfg := serveClientConfig(*serve, *token)
	c := &mcpClient{serveURL: cfg.serveURL, token: cfg.token, http: &http.Client{}}

	ok := true
	for _, f := range multiFlagValue {
		data, err := os.ReadFile(f)
		if err != nil {
			emitApplyResult(false, f, []fieldError{{Path: "file", Message: err.Error()}})
			ok = false
			continue
		}
		artifact, err := parseArtifact(data)
		if err != nil {
			emitApplyResult(false, f, []fieldError{{Path: "kind", Message: err.Error()}})
			ok = false
			continue
		}
		if errs := artifact.validate(); len(errs) > 0 {
			emitApplyResult(false, f, errs)
			ok = false
			continue
		}
		if err := c.applySemantic(context.Background(), artifact); err != nil {
			emitApplyResult(false, f, []fieldError{{Path: artifact.kind, Message: err.Error()}})
			ok = false
			continue
		}
		action, err := applyOne(c, artifact)
		if err != nil {
			emitApplyResult(false, f, []fieldError{{Path: artifact.collection, Message: err.Error()}})
			ok = false
			continue
		}
		emitApplyResult(true, f, nil, map[string]any{
			"artifact": artifact.kind + "/" + artifact.name,
			"action":   action,
		})
	}
	if !ok {
		os.Exit(2)
	}
}

// multiFlag: flag.Value 收集重复 -f。
type multiFlag struct{}

var multiFlagValue []string

func (m *multiFlag) String() string { return "" }
func (m *multiFlag) Set(s string) error {
	multiFlagValue = append(multiFlagValue, s)
	return nil
}

func serveClientConfig(serve, token string) struct{ serveURL, token string } {
	cfg := struct{ serveURL, token string }{serve, token}
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
		fmt.Fprintln(os.Stderr, "apply: 需要 PB token（-token 或环境变量 PDUCK_TOKEN）")
		os.Exit(2)
	}
	return cfg
}

// emitApplyResult: 单文件结果结构化输出（成功带 artifact/action，失败带 errors）。
func emitApplyResult(ok bool, file string, errs []fieldError, extra ...map[string]any) {
	out := map[string]any{"ok": ok, "file": file}
	if ok {
		for k, v := range extra[0] {
			out[k] = v
		}
	} else {
		out["errors"] = errs
	}
	enc, _ := json.Marshal(out)
	fmt.Println(string(enc))
}
