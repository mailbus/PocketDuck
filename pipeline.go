package main

// pipeline.go: 票11 管道引擎——固化能自动跑。
// 执行器顺序执行任务（v0.x 无 DAG，规格 v0x 定案）；调度器 serve 内嵌 worker 30s 轮询
// cron 到期（robfig/cron 仅作解析）；快照=同名覆盖 latest parquet；运行历史落 pipeline_runs。
// 全程只用只读 DuckDB 连接（探针实证：RO 连接可 COPY TO 外部 parquet），
// DuckDB 文件在 serve 生命周期内只以 RO 打开一次（单写者线的 RO 面）。

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"github.com/robfig/cron/v3"
)

type pipelineSpec struct {
	Name     string
	Schedule string
	Tasks    []map[string]any
}

// pipelineSpecFromRecord: PB 记录（map 或 core.Record）→ spec；tasks 兼容 []any 与 JSON 字符串两种形态。
func pipelineSpecFromRecord(rec map[string]any) (*pipelineSpec, error) {
	name, _ := rec["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("pipeline 缺 name")
	}
	schedule, _ := rec["schedule"].(string)
	var tasks []map[string]any
	switch t := rec["tasks"].(type) {
	case string:
		if err := json.Unmarshal([]byte(t), &tasks); err != nil {
			return nil, fmt.Errorf("pipeline %q tasks 解析失败: %w", name, err)
		}
	case []any:
		for _, item := range t {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("pipeline %q task 非对象", name)
			}
			tasks = append(tasks, m)
		}
	default:
		return nil, fmt.Errorf("pipeline %q 缺 tasks", name)
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("pipeline %q 无任务", name)
	}
	return &pipelineSpec{Name: name, Schedule: schedule, Tasks: tasks}, nil
}

func pipelineSpecFromRecord2(rec *core.Record) (*pipelineSpec, error) {
	var tasks any = rec.Get("tasks")
	switch tasks.(type) {
	case string, []any, nil:
		// 常见形态直传
	default:
		if jb, err := json.Marshal(tasks); err == nil {
			tasks = string(jb) // PB 内部形态（如 types.JSON）→ 归一为 JSON 字符串
		}
	}
	return pipelineSpecFromRecord(map[string]any{
		"name":     rec.GetString("name"),
		"schedule": rec.GetString("schedule"),
		"tasks":    tasks,
	})
}

// resolveFn: 任务值 → SQL（saved_query 引用优先，未命中按原始 SQL 处理）。
type resolveFn func(value string) (string, error)

func testResolver(refs map[string]string) resolveFn {
	return func(value string) (string, error) {
		if sql, ok := refs[value]; ok {
			return sql, nil
		}
		return value, nil // 未命中引用 → 原样作为 SQL
	}
}

// histFn: 运行历史回调（worker 写 PB collection；CLI 写 PB API；测试捕获）。
type histFn func(status string, rows, elapsedMS int64, checksum, snapshot, errMsg string)

func snapshotDir() string {
	if d := os.Getenv("PDUCK_SNAPSHOT_DIR"); d != "" {
		return d
	}
	return "data/snapshots"
}

// parseSchedule: 标准 5 段 cron（robfig/cron 仅作解析器，调度轮询自管）。
func parseSchedule(expr string) (cron.Schedule, error) {
	return cron.ParseStandard(expr)
}

// runPipeline: 顺序执行任务；query 任务产出快照（同名覆盖 latest）；
// export 任务复用票05/08（用上一个 query 任务的 SQL）；任一任务失败即停并记 error。
func runPipeline(db *sql.DB, resolve resolveFn, hist histFn, spec *pipelineSpec) error {
	start := time.Now()
	status, errMsg := "success", ""
	var rows int64
	checksum, snapshotPath, lastSQL := "", "", ""

	for i, task := range spec.Tasks {
		if qv, ok := task["query"]; ok {
			q, _ := qv.(string)
			sqlText, err := resolve(q)
			if err != nil {
				status, errMsg = "error", fmt.Sprintf("task %d: %v", i, err)
				break
			}
			snapshotPath = filepath.ToSlash(filepath.Join(snapshotDir(), spec.Name+".parquet"))
			if err := os.MkdirAll(filepath.Dir(snapshotPath), 0o755); err != nil {
				status, errMsg = "error", fmt.Sprintf("task %d: %v", i, err)
				break
			}
			if err := exportParquet(db, sqlText, snapshotPath); err != nil {
				status, errMsg = "error", fmt.Sprintf("task %d: %v", i, err)
				break
			}
			if err := db.QueryRow("SELECT COUNT(*) FROM read_parquet(?)", snapshotPath).Scan(&rows); err != nil {
				status, errMsg = "error", fmt.Sprintf("task %d: 快照计数: %v", i, err)
				break
			}
			checksum = fileSHA256(snapshotPath)
			lastSQL = sqlText
			continue
		}
		if ev, ok := task["export"]; ok {
			e, _ := ev.(string)
			if !startsWith(e, "to=") || lastSQL == "" {
				status, errMsg = "error", fmt.Sprintf("task %d: export 需 to= 且前置 query 任务", i)
				break
			}
			if err := Export(db, lastSQL, e[3:]); err != nil {
				status, errMsg = "error", fmt.Sprintf("task %d: %v", i, err)
				break
			}
			continue
		}
		status, errMsg = "error", fmt.Sprintf("task %d: 未知任务（支持 query/export）", i)
		break
	}

	hist(status, rows, time.Since(start).Milliseconds(), checksum, snapshotPath, errMsg)
	return nil
}

func fileSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// startPipelineWorker: serve 内嵌调度器。30s 轮询 pipelines，cron 到期即执行；
// map 内存记录下次触发时间；重启后从 PB 重载（schedule 不丢，规格通过线 4）。
func startPipelineWorker(app core.App, db *sql.DB) {
	next := map[string]time.Time{}
	for {
		time.Sleep(30 * time.Second)
		records, err := app.FindRecordsByFilter("pipelines", "schedule != ''", "", 200, 0)
		if err != nil {
			continue // collection 未建等 → 下轮再试
		}
		now := time.Now()
		for _, rec := range records {
			spec, err := pipelineSpecFromRecord2(rec)
			if err != nil {
				log.Printf("pipeline worker: %v", err)
				continue
			}
			sched, err := parseSchedule(spec.Schedule)
			if err != nil {
				log.Printf("pipeline worker: %q schedule 非法: %v", spec.Name, err)
				continue
			}
			n, seen := next[spec.Name]
			if !seen {
				n = sched.Next(now)
				next[spec.Name] = n
				continue
			}
			if !now.Before(n) {
				go func(spec *pipelineSpec) {
					resolve := func(value string) (string, error) {
						rec, err := app.FindRecordsByFilter("saved_queries", "name = {:name}", "", 1, 0, dbx.Params{"name": value})
						if err != nil || len(rec) == 0 {
							return value, nil // 未命中引用 → 原始 SQL
						}
						return rec[0].GetString("sql"), nil
					}
					hist := func(status string, rows, elapsedMS int64, checksum, snapshot, errMsg string) {
						col, err := app.FindCollectionByNameOrId("pipeline_runs")
						if err != nil {
							return
						}
						r := core.NewRecord(col)
						r.Set("pipeline", spec.Name)
						r.Set("status", status)
						r.Set("rows", rows)
						r.Set("elapsed_ms", elapsedMS)
						r.Set("checksum", checksum)
						r.Set("snapshot", snapshot)
						r.Set("error", errMsg)
						if err := app.Save(r); err != nil {
							log.Printf("pipeline worker: 写历史失败: %v", err)
						}
					}
					if err := runPipeline(db, resolve, hist, spec); err != nil {
						log.Printf("pipeline worker: %v", err)
					}
				}(spec)
				next[spec.Name] = sched.Next(now)
			}
		}
	}
}

// runCmd: pduck run -name <pipeline>——单管道一次性执行（调试壳，与 worker 共用 runPipeline）。
func runCmd() {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	name := fs.String("name", "", "管道名（apply 固化的 pipelines 记录）")
	serve := fs.String("serve", "", "pduck serve 地址（默认 http://127.0.0.1:8090）")
	token := fs.String("token", "", "PB token（默认读 PDUCK_TOKEN）")
	fs.Parse(os.Args[2:])
	if *name == "" {
		fs.Usage()
		os.Exit(2)
	}
	cfg := serveClientConfig(*serve, *token)
	c := &mcpClient{serveURL: cfg.serveURL, token: cfg.token, http: &http.Client{}}

	rec, err := c.findRecord(context.Background(), "pipelines", *name)
	if err != nil {
		log.Fatalf("run: %v", err)
	}
	spec, err := pipelineSpecFromRecord(rec)
	if err != nil {
		log.Fatalf("run: %v", err)
	}

	db, err := openDuckDB("data/warehouse.duckdb") // 只读连接足够（探针：RO 可 COPY TO）
	if err != nil {
		log.Fatalf("run: %v", err)
	}
	defer db.Close()

	resolve := func(value string) (string, error) {
		r, err := c.findRecord(context.Background(), "saved_queries", value)
		if err != nil {
			return value, nil // 未命中引用 → 原始 SQL
		}
		sql, _ := r["sql"].(string)
		return sql, nil
	}
	hist := func(status string, rows, elapsedMS int64, checksum, snapshot, errMsg string) {
		payload := map[string]any{
			"pipeline": spec.Name, "status": status, "rows": rows,
			"elapsed_ms": elapsedMS, "checksum": checksum, "snapshot": snapshot, "error": errMsg,
		}
		if _, err := c.sendRecord(http.MethodPost, "pipeline_runs/records", payload); err != nil {
			log.Printf("run: 写历史失败: %v", err)
		}
		fmt.Printf("run %s: %s rows=%d elapsed=%dms snapshot=%s\n", spec.Name, status, rows, elapsedMS, snapshot)
	}
	if err := runPipeline(db, resolve, hist, spec); err != nil {
		log.Fatal(err)
	}
}
