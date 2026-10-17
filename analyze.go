package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
)

const maxRows = 10000
const defaultTimeoutMS = 30000
const maxTimeoutMS = 300000

type analyzeRequest struct {
	SQL       string `json:"sql"`
	Params    []any  `json:"params"`
	TimeoutMS int    `json:"timeout_ms"`
}

// openDuckDB 只读打开 DuckDB 文件；文件不存在则先以读写模式创建一次。
// 只读是兜底约束（规格 §5 数据线），写 SQL 一律拒绝。
func openDuckDB(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll("data", 0o755); err != nil {
			return nil, err
		}
		init, err := sql.Open("duckdb", path)
		if err != nil {
			return nil, err
		}
		init.Close() // 产生文件后关闭
	}
	// ponytail: 单连接池+只读，够 v0；多写者/并发池化等真实负载再说
	return sql.Open("duckdb", path+"?access_mode=read_only")
}

// openDuckDBRW: 写侧 CLI 专用（票06 import）；与只读的 openDuckDB 刻意分开，
// 避免写权限渗回 /api/analyze 路径。
func openDuckDBRW(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll("data", 0o755); err != nil {
			return nil, err
		}
		init, err := sql.Open("duckdb", path)
		if err != nil {
			return nil, err
		}
		init.Close() // 产生文件后关闭
	}
	return sql.Open("duckdb", path)
}

// normalizeRow 让值可 JSON 序列化：[]byte 转 string，其余原样
func normalizeRow(vals []any) []any {
	for i, v := range vals {
		if b, ok := v.([]byte); ok {
			vals[i] = string(b)
		}
	}
	return vals
}

// bindAnalyze 注册 POST /api/analyze（票02）：
// PB token 鉴权 → 只读 DuckDB 执行 → JSON 结果，超时/行数上限兜底。
func bindAnalyze(db *sql.DB) func(*core.ServeEvent) error {
	return func(se *core.ServeEvent) error {
		se.Router.POST("/api/analyze", func(e *core.RequestEvent) error {
			var req analyzeRequest
			if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
				return e.BadRequestError("invalid JSON body", err)
			}
			if req.SQL == "" {
				return e.BadRequestError("sql is required", nil)
			}
			if req.TimeoutMS <= 0 {
				req.TimeoutMS = defaultTimeoutMS
			}
			if req.TimeoutMS > maxTimeoutMS {
				req.TimeoutMS = maxTimeoutMS
			}

			ctx, cancel := context.WithTimeout(e.Request.Context(), time.Duration(req.TimeoutMS)*time.Millisecond)
			defer cancel()

			start := time.Now()
			rows, err := db.QueryContext(ctx, req.SQL, req.Params...)
			if err != nil {
				return e.BadRequestError("query failed: "+err.Error(), nil)
			}
			defer rows.Close()

			columns, err := rows.Columns()
			if err != nil {
				return e.BadRequestError("query failed: "+err.Error(), nil)
			}

			data := make([][]any, 0, 128)
			truncated := false
			for rows.Next() {
				if len(data) >= maxRows {
					truncated = true
					break
				}
				vals := make([]any, len(columns))
				ptrs := make([]any, len(columns))
				for i := range ptrs {
					ptrs[i] = &vals[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					return e.BadRequestError("scan failed: "+err.Error(), nil)
				}
				data = append(data, normalizeRow(vals))
			}
			if err := rows.Err(); err != nil {
				return e.BadRequestError("query failed: "+err.Error(), nil)
			}

			return e.JSON(http.StatusOK, map[string]any{
				"columns":    columns,
				"rows":       data,
				"row_count":  len(data),
				"elapsed_ms": time.Since(start).Milliseconds(),
				"truncated":  truncated,
			})
		}).Bind(apis.RequireAuth())
		return se.Next()
	}
}
