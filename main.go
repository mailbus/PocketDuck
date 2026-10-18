package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/pocketbase/pocketbase"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
	_ "github.com/marcboeker/go-duckdb/v2"
)

// duckcheck 验证 DuckDB 运行时已链接进二进制（票01 验收项）
func duckcheck() {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		log.Fatalf("duckdb open: %v", err)
	}
	defer db.Close()
	var one int
	if err := db.QueryRow("SELECT 1").Scan(&one); err != nil {
		log.Fatalf("duckdb query: %v", err)
	}
	fmt.Printf("duckdb ok: SELECT 1 -> %d\n", one)
}

// exportCmd: 票05 写侧 CLI——写操作不进 PB 请求路径（宪法同款纪律）。
// 用法: pduck export -sql "SELECT ..." -to out.parquet
//       pduck export -sql "SELECT ..." -to out.xlsx
//       pduck export -sql "SELECT ..." -to postgres://user:pass@host:port/db/表名
func exportCmd() {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	sqlQ := fs.String("sql", "", "导出的 SELECT 查询")
	to := fs.String("to", "", "目标: <path>.parquet 或 postgres://user:pass@host:port/db/表名")
	fs.Parse(os.Args[2:])
	if *sqlQ == "" || *to == "" {
		fs.Usage()
		os.Exit(2)
	}
	duckDB, err := openDuckDB("data/warehouse.duckdb")
	if err != nil {
		log.Fatalf("open duckdb: %v", err)
	}
	defer duckDB.Close()
	if err := Export(duckDB, *sqlQ, *to); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("exported -> %s\n", *to)
}

// importCmd: 票06 读侧 CLI——外部 PG/MySQL 表拉进 warehouse。
// 用法: pduck import -from "postgres://user:pass@host:port/db/表名" -into 表名
//       pduck import -from "mysql://user:pass@host:port/db/表名" -into 表名
func importCmd() {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	from := fs.String("from", "", "来源: postgres://host/db/表名 或 mysql://host/db/表名")
	into := fs.String("into", "", "warehouse 中的目标表名")
	fs.Parse(os.Args[2:])
	if *from == "" || *into == "" {
		fs.Usage()
		os.Exit(2)
	}
	duckDB, err := openDuckDBRW("data/warehouse.duckdb") // import 需建表，走读写打开
	if err != nil {
		log.Fatalf("open duckdb: %v", err)
	}
	defer duckDB.Close()
	if err := Import(duckDB, *from, *into); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("imported -> warehouse table %q\n", *into)
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Println("pduck v0.1.0 (PocketBase + DuckDB)")
			return
		case "duckcheck":
			duckcheck()
			return
		case "export":
			exportCmd()
			return
		case "import":
			importCmd()
			return
		case "mcp":
			mcpCmd()
			return
		case "apply":
			applyCmd()
			return
		case "run":
			runCmd()
			return
		}
	}

	app := pocketbase.New()

	// 票02: 只读 DuckDB + /api/analyze（独立路由，不进 PB 热路径）
	duckDB, err := openDuckDB("data/warehouse.duckdb")
	if err != nil {
		log.Fatalf("open duckdb: %v", err)
	}
	defer duckDB.Close()
	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func:     bindAnalyze(duckDB),
		Priority: -100,
	})

	// 票09/10: 固化产物集合引导（saved_queries / views / pipelines）
	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(e *core.ServeEvent) error {
			cols := []struct {
				name   string
				fields []core.Field
				index  string
			}{
				{"saved_queries", []core.Field{
					&core.TextField{Name: "name", Required: true},
					&core.TextField{Name: "sql", Required: true},
					&core.JSONField{Name: "params"},
					&core.TextField{Name: "description"},
				}, "idx_saved_queries_name"},
				{"views", []core.Field{
					&core.TextField{Name: "name", Required: true},
					&core.TextField{Name: "query", Required: true},
					&core.JSONField{Name: "chart", Required: true},
				}, "idx_views_name"},
				{"pipelines", []core.Field{
					&core.TextField{Name: "name", Required: true},
					&core.TextField{Name: "schedule"},
					&core.JSONField{Name: "tasks", Required: true},
				}, "idx_pipelines_name"},
				{"pipeline_runs", []core.Field{
					&core.TextField{Name: "pipeline", Required: true},
					&core.TextField{Name: "status", Required: true},
					&core.NumberField{Name: "rows"},
					&core.NumberField{Name: "elapsed_ms"},
					&core.TextField{Name: "checksum"},
					&core.TextField{Name: "snapshot"},
					&core.TextField{Name: "error"},
				}, ""},
			}
			for _, c := range cols {
				if _, err := e.App.FindCollectionByNameOrId(c.name); err != nil {
					col := core.NewCollection(core.CollectionTypeBase, c.name)
					col.Fields.Add(c.fields...)
					if c.index != "" {
						col.AddIndex(c.index, true, "name", "")
					}
					if err := e.App.Save(col); err != nil {
						return err
					}
				}
			}
			return e.Next()
		},
		Priority: -99,
	})

	// 票11: 内嵌管道调度 worker（30s 轮询 cron 到期；schedule 持久于 pipelines 集合，重启自动恢复）
	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(e *core.ServeEvent) error {
			go startPipelineWorker(e.App, duckDB)
			return e.Next()
		},
		Priority: -98,
	})

	// 静态服务 pb_public/（v0.40 需显式绑定；对齐官方 examples/base/main.go）
	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Func: func(e *core.ServeEvent) error {
			if !e.Router.HasRoute(http.MethodGet, "/{path...}") {
				e.Router.GET("/{path...}", apis.Static(os.DirFS("pb_public"), false))
			}
			return e.Next()
		},
		Priority: 999,
	})

	// 票02: 在此注册 /api/analyze 只读 SQL API

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
