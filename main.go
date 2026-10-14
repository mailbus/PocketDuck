package main

import (
	"database/sql"
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

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Println("pduck v0.1.0 (PocketBase + DuckDB)")
			return
		case "duckcheck":
			duckcheck()
			return
		}
	}

	app := pocketbase.New()

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
