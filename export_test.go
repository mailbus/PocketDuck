package main

import (
	"database/sql"
	"path/filepath"
	"testing"

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
