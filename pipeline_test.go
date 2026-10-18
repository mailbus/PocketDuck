package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// 票11: 管道执行器——query 任务(快照)/export 任务/失败即停，历史经 histFn 回调。

func testHist(t *testing.T) (func(string, int64, int64, string, string, string), *[]string) {
	t.Helper()
	var calls []string
	return func(status string, rows, elapsedMS int64, checksum, snapshot, errMsg string) {
		calls = append(calls, status+"|"+checksum)
	}, &calls
}

func TestRunPipelineQuerySnapshot(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	os.Setenv("PDUCK_SNAPSHOT_DIR", dir)
	defer os.Unsetenv("PDUCK_SNAPSHOT_DIR")

	hist, calls := testHist(t)
	spec := &pipelineSpec{Name: "p1", Tasks: []map[string]any{
		{"query": "SELECT 42 AS n"},
	}}
	if err := runPipeline(db, testResolver(nil), hist, spec); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "success|") {
		t.Fatalf("历史记录不对: %v", *calls)
	}
	snap := filepath.Join(dir, "p1.parquet")
	if _, err := os.Stat(snap); err != nil {
		t.Fatalf("快照未产出: %v", err)
	}
	// 读回对账
	var n int
	if err := db.QueryRow("SELECT n FROM read_parquet(?)", snap).Scan(&n); err != nil || n != 42 {
		t.Fatalf("快照读回: n=%d err=%v", n, err)
	}
}

func TestRunPipelineRefAndExport(t *testing.T) {
	db, _ := sql.Open("duckdb", "")
	defer db.Close()
	dir := t.TempDir()
	os.Setenv("PDUCK_SNAPSHOT_DIR", dir)
	defer os.Unsetenv("PDUCK_SNAPSHOT_DIR")

	out := filepath.Join(dir, "out.xlsx")
	hist, _ := testHist(t)
	spec := &pipelineSpec{Name: "p2", Tasks: []map[string]any{
		{"query": "saved-demo"}, // 引用 named query → resolver 解析
		{"export": "to=" + filepath.ToSlash(out)},
	}}
	resolver := testResolver(map[string]string{"saved-demo": "SELECT 'x' AS s"})
	if err := runPipeline(db, resolver, hist, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("export 任务未产出: %v", err)
	}
}

func TestRunPipelineErrorStops(t *testing.T) {
	db, _ := sql.Open("duckdb", "")
	defer db.Close()
	os.Setenv("PDUCK_SNAPSHOT_DIR", t.TempDir())
	defer os.Unsetenv("PDUCK_SNAPSHOT_DIR")

	var status, errMsg string
	hist := func(s string, rows, e int64, c, snap, em string) { status, errMsg = s, em }
	spec := &pipelineSpec{Name: "p3", Tasks: []map[string]any{
		{"query": "SELECT * FROM no_such_table"},
		{"export": "to=nope.xlsx"},
	}}
	if err := runPipeline(db, testResolver(nil), hist, spec); err != nil {
		t.Fatal(err)
	}
	if status != "error" || !strings.Contains(errMsg, "no_such_table") {
		t.Fatalf("期望 error 状态带表名, got %q / %q", status, errMsg)
	}
	if _, err := os.Stat("nope.xlsx"); err == nil {
		t.Fatal("失败后不应继续执行后续任务")
	}
}

func TestPipelineSpecFromRecord(t *testing.T) {
	spec, err := pipelineSpecFromRecord(map[string]any{
		"name":     "w",
		"schedule": "0 8 * * 1",
		"tasks":    []any{map[string]any{"query": "a"}, map[string]any{"export": "to=b.parquet"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "w" || len(spec.Tasks) != 2 {
		t.Fatalf("spec 解析不对: %+v", spec)
	}
	// tasks 以 JSON 字符串形态到达（PB JSONField 另一形态）
	if _, err := pipelineSpecFromRecord(map[string]any{
		"name": "w2", "tasks": `[{"query":"a"}]`,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineCronDue(t *testing.T) {
	sched, err := parseSchedule("* * * * *")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	next := sched.Next(now)
	if !next.After(now) {
		t.Fatal("Next 应晚于 now")
	}
	if _, err := parseSchedule("not a cron"); err == nil {
		t.Fatal("非法 cron 应报错")
	}
}
