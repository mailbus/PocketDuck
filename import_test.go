package main

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/marcboeker/go-duckdb/v2"
)

var wantRows = []struct {
	id   int
	boro string
}{
	{1, "Manhattan"}, {2, "Queens"}, {3, "Brooklyn"},
}

func wantTable(t *testing.T, db *sql.DB, create, insert string) {
	if _, err := db.Exec(create); err != nil {
		t.Fatal(err)
	}
	for _, w := range wantRows {
		if _, err := db.Exec(insert, w.id, w.boro); err != nil {
			t.Fatal(err)
		}
	}
}

func assertRoundtrip(t *testing.T, duck *sql.DB, table string) {
	t.Helper()
	rows, err := duck.Query(`SELECT id, borough FROM ` + table + ` ORDER BY id`)
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
	if len(got) != len(wantRows) {
		t.Fatalf("%s rows: got %d want %d", table, len(got), len(wantRows))
	}
	for i := range wantRows {
		if got[i].id != wantRows[i].id || got[i].boro != wantRows[i].boro {
			t.Fatalf("%s row %d: got %+v want %+v", table, i, got[i], wantRows[i])
		}
	}
}

// 票06 任务1.1: PG 表导入 warehouse，逐行一致
func TestImportPGRoundtrip(t *testing.T) {
	dsn := os.Getenv("PDUCK_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("PDUCK_TEST_PG_DSN 未设置，跳过（用 scripts/test_import.sh 起 PG）")
	}
	pg, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	wantTable(t, pg, `CREATE TABLE rt_src(id INT PRIMARY KEY, borough TEXT)`,
		`INSERT INTO rt_src VALUES ($1, $2)`)

	duck, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer duck.Close()
	if err := Import(duck, strings.Replace(dsn, "?", "/rt_src?", 1), "rt_import"); err != nil {
		t.Fatalf("import from pg: %v", err)
	}
	assertRoundtrip(t, duck, "rt_import")
}

// 票06 任务2.1: MySQL 表导入 warehouse，逐行一致
func TestImportMySQLRoundtrip(t *testing.T) {
	dsn := os.Getenv("PDUCK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("PDUCK_TEST_MYSQL_DSN 未设置，跳过（用 scripts/test_import.sh 起 MySQL）")
	}
	duck, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer duck.Close()
	if err := Import(duck, dsn+"/rt_src", "my_import"); err != nil {
		t.Fatalf("import from mysql: %v", err)
	}
	assertRoundtrip(t, duck, "my_import")
}
