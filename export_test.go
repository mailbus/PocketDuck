package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/marcboeker/go-duckdb/v2"
)

// 票05 任务2.1: Parquet 写出 roundtrip 逐行一致
func TestExportParquetRoundtrip(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	src := []struct {
		id    int
		boro  string
		fare  float64
	}{}
	want := []struct {
		id   int
		boro string
	}{
		{1, "Manhattan"}, {2, "Queens"}, {3, "Brooklyn"},
	}
	if _, err := db.Exec(`CREATE TABLE t(id INT, borough VARCHAR, fare DOUBLE)`);
		err != nil {
		t.Fatal(err)
	}
	for _, w := range want {
		if _, err := db.Exec(`INSERT INTO t VALUES (?, ?, ?)`, w.id, w.boro, 7.5); err != nil {
			t.Fatal(err)
		}
	}
	_ = src

	out := filepath.Join(t.TempDir(), "out.parquet")
	if err := Export(db, "SELECT id, borough, fare FROM t ORDER BY id", out); err != nil {
		t.Fatalf("export: %v", err)
	}

	rows, err := db.Query(`SELECT id, borough FROM read_parquet(?) ORDER BY id`, out)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []struct {
		id   int
		boro string
	}
	for rows.Next() {
		var g struct {
			id   int
			boro string
		}
		if err := rows.Scan(&g.id, &g.boro); err != nil {
			t.Fatal(err)
		}
		got = append(got, g)
	}
	if len(got) != len(want) {
		t.Fatalf("roundtrip rows: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

// 票05 任务2.2: PG 写出 roundtrip 逐行一致（需真 PG，由 scripts/test_export.sh 提供 DSN）
func TestExportPostgresRoundtrip(t *testing.T) {
	dsn := os.Getenv("PDUCK_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("PDUCK_TEST_PG_DSN 未设置，跳过（用 scripts/test_export.sh 起 PG）")
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE t(id INT, borough VARCHAR, fare DOUBLE)`); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		id   int
		boro string
	}{
		{1, "Manhattan"}, {2, "Queens"}, {3, "Brooklyn"},
	}
	for _, w := range want {
		if _, err := db.Exec(`INSERT INTO t VALUES (?, ?, ?)`, w.id, w.boro, 9.9); err != nil {
			t.Fatal(err)
		}
	}

	dest := strings.Replace(dsn, "?", "/rt_roundtrip?", 1) // 表名插在查询参数前
	if err := Export(db, "SELECT id, borough, fare FROM t ORDER BY id", dest); err != nil {
		t.Fatalf("export to pg: %v", err)
	}

	pg, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	rows, err := pg.Query(`SELECT id, borough FROM rt_roundtrip ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []struct {
		id   int
		boro string
	}
	for rows.Next() {
		var g struct {
			id   int
			boro string
		}
		if err := rows.Scan(&g.id, &g.boro); err != nil {
			t.Fatal(err)
		}
		got = append(got, g)
	}
	if len(got) != len(want) {
		t.Fatalf("pg roundtrip rows: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pg row %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

// 票08: xlsx 写出 → DuckDB read_xlsx 第二读侧逐行对账（写读两侧不同实现）
func TestExportXlsxRoundtrip(t *testing.T) {
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`LOAD excel`); err != nil {
		t.Skipf("excel 扩展不可用（对账读侧）: %v", err)
	}

	if _, err := db.Exec(`CREATE TABLE t(id BIGINT, fare DOUBLE, ok BOOLEAN, note VARCHAR, d DATE)`);
		err != nil {
		t.Fatal(err)
	}
	ins, err := db.Prepare(`INSERT INTO t VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range []struct {
		id   int64
		fare float64
		ok   bool
		note string
		d    string
	}{{1, 7.5, true, "Manhattan", "2025-01-15"}, {2, 12.25, false, "Queens", "2025-01-16"}, {3, 0, true, "", "2025-01-31"}} {
		if _, err := ins.Exec(r.id, r.fare, r.ok, r.note, r.d); err != nil {
			t.Fatal(err)
		}
		_ = i
	}

	out := filepath.Join(t.TempDir(), "out.xlsx")
	if err := Export(db, "SELECT id, fare, ok, note, d FROM t ORDER BY id", out); err != nil {
		t.Fatalf("export xlsx: %v", err)
	}

	// 第二读侧: DuckDB read_xlsx
	// 类型保真: 数字单元格必须以 DOUBLE 回来——若写侧把 float 降级成文本，这里先炸
	var fareType string
	if err := db.QueryRow(`SELECT column_type FROM (DESCRIBE SELECT * FROM read_xlsx(?, header = true)) WHERE column_name = 'fare'`, out).Scan(&fareType); err != nil {
		t.Fatal(err)
	}
	if fareType != "DOUBLE" {
		t.Fatalf("类型保真失败: fare 列读回 %s, want DOUBLE（浮点被降级为文本单元格）", fareType)
	}

	rows, err := db.Query(`SELECT * FROM read_xlsx(?, header = true) ORDER BY id`, out)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cts, err := rows.ColumnTypes()
	if err != nil {
		t.Fatal(err)
	}
	var got [][]string
	for rows.Next() {
		vals := make([]any, len(cts))
		for i := range vals {
			vals[i] = new(any)
		}
		if err := rows.Scan(vals...); err != nil {
			t.Fatal(err)
		}
		row := make([]string, len(vals))
		for i, v := range vals {
			// read_xlsx 对日期单元格返回 time.Time，统一格式化后再比
			if tm, ok := (*(v.(*any))).(time.Time); ok {
				row[i] = tm.Format("2006-01-02 15:04:05")
			} else {
				row[i] = fmt.Sprint(*(v.(*any)))
			}
		}
		got = append(got, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	want := [][]string{
		{"1", "7.5", "true", "Manhattan", "2025-01-15 00:00:00"},
		{"2", "12.25", "false", "Queens", "2025-01-16 00:00:00"},
		{"3", "0", "true", "", "2025-01-31 00:00:00"},
	}
	if len(got) != len(want) {
		t.Fatalf("行数: got %d want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		for j := range want[i] {
			if got[i][j] != want[i][j] {
				t.Errorf("row %d col %d: got %q want %q", i, j, got[i][j], want[i][j])
			}
		}
	}
}
