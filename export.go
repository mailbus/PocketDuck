package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/marcboeker/go-duckdb/v2"
	"github.com/xuri/excelize/v2"
)

// export.go: 票05 写侧流水线——DuckDB 查询结果写出。
// 可插拔扩展点是 dest 字符串（宪法: 依赖克制，sink 按前缀分发）:
//   - <path>.parquet  → DuckDB COPY TO parquet（parquet 扩展静态链接，无外部依赖）
//   - <path>.xlsx     → 票08: Go 侧 excelize 流式写出（探针实证 excel 扩展仅 read_xlsx 无写侧）
//   - postgres://...  → 任务2.2: DuckDB COPY TO csv → pgx COPY FROM STDIN
//   - s3://...        → v0.x: httpfs 扩展接真对象存储

// Export 将 query 结果写出到 dest。
func Export(db *sql.DB, query, dest string) error {
	switch {
	case strings.HasSuffix(dest, ".parquet"):
		return exportParquet(db, query, dest)
	case strings.HasSuffix(dest, ".xlsx"):
		return exportXlsx(db, query, dest)
	case strings.HasPrefix(dest, "postgres://"), strings.HasPrefix(dest, "postgresql://"):
		return exportPostgres(db, query, dest)
	default:
		return fmt.Errorf("unsupported dest %q: 仅支持 *.parquet、*.xlsx 与 postgres://", dest)
	}
}

// exportPostgres: dest = postgres://user:pass@host:port/db/表名。
// 流水线: DuckDB 查 schema → 建表 → COPY 结果到临时 CSV → pgx COPY FROM STDIN。
// ponytail: CSV 全量落盘再导入，10 万行内存/磁盘压力可忽略；上百万行改流式管道。
func exportPostgres(db *sql.DB, query, dest string) error {
	dsn, table, err := splitTableRef(dest)
	if err != nil {
		return fmt.Errorf("pg dest: %w", err)
	}

	// 1. 从 DuckDB 取查询 schema，映射 PG 类型建表
	rows, err := db.Query("DESCRIBE " + query)
	if err != nil {
		return fmt.Errorf("describe: %w", err)
	}
	type col struct{ name, typ string }
	var cols []col
	for rows.Next() {
		var name, ctype string
		var null, key, def, extra any
		if err := rows.Scan(&name, &ctype, &null, &key, &def, &extra); err != nil {
			rows.Close()
			return fmt.Errorf("describe scan: %w", err)
		}
		cols = append(cols, col{name, ctype})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(cols) == 0 {
		return fmt.Errorf("查询无结果列: %s", query)
	}

	pg, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		return fmt.Errorf("pg connect: %w", err)
	}
	defer pg.Close(context.Background())

	var defs []string
	for _, c := range cols {
		defs = append(defs, fmt.Sprintf(`"%s" %s`, c.name, pgType(c.typ)))
	}
	ddl := fmt.Sprintf(`DROP TABLE IF EXISTS "%s"; CREATE TABLE "%s" (%s)`, table, table, strings.Join(defs, ", "))
	if _, err := pg.Exec(context.Background(), ddl); err != nil {
		return fmt.Errorf("pg create table: %w", err)
	}

	// 2. DuckDB COPY 到临时 CSV
	tmp, err := os.CreateTemp("", "pduck-export-*.csv")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	if _, err := db.Exec(fmt.Sprintf("COPY (%s) TO '%s' (FORMAT csv, HEADER true)", query, filepath.ToSlash(tmp.Name()))); err != nil {
		return fmt.Errorf("copy csv: %w", err)
	}

	// 3. PG COPY FROM STDIN 批量导入
	f, err := os.Open(tmp.Name())
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = pg.PgConn().CopyFrom(context.Background(), f,
		fmt.Sprintf(`COPY "%s" FROM STDIN WITH (FORMAT csv, HEADER true)`, table))
	if err != nil {
		return fmt.Errorf("pg copy from: %w", err)
	}
	return nil
}

// pgType: DuckDB → PostgreSQL 类型映射（未列出的归 text 兜底）。
func pgType(duckType string) string {
	switch strings.ToUpper(duckType) {
	case "INTEGER", "INT32":
		return "integer"
	case "BIGINT", "INT64", "HUGEINT":
		return "bigint"
	case "DOUBLE", "FLOAT", "REAL":
		return "double precision"
	case "BOOLEAN", "BOOL":
		return "boolean"
	case "DATE":
		return "date"
	case "TIMESTAMP", "TIMESTAMP WITH TIME ZONE", "DATETIME":
		return "timestamp"
	case "DECIMAL", "DECIMAL(width, scale)":
		return "numeric"
	default:
		if strings.HasPrefix(strings.ToUpper(duckType), "DECIMAL") {
			return "numeric"
		}
		return "text"
	}
}

// exportParquet: COPY 直接落 parquet 文件。
func exportParquet(db *sql.DB, query, path string) error {
	q := fmt.Sprintf("COPY (%s) TO '%s' (FORMAT parquet)", query, path)
	if _, err := db.Exec(q); err != nil {
		return fmt.Errorf("copy parquet: %w", err)
	}
	return nil
}

// exportXlsx: excelize StreamWriter 流式写出（10 万行不吃内存）。
// go-duckdb 扫描 any 的具体类型先归一（Double 进 any 是 duckdb.Decimal 不是 float64，
// excelize 不认识会降级成字符串单元格），保数字/布尔/日期列的单元格类型。
func exportXlsx(db *sql.DB, query, path string) error {
	rows, err := db.Query(query)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	toCell := func(v any) any {
		switch t := v.(type) {
		case duckdb.Decimal:
			return t.Float64()
		case []byte:
			return string(t)
		default:
			return v
		}
	}

	f := excelize.NewFile()
	defer f.Close()
	sw, err := f.NewStreamWriter("Sheet1")
	if err != nil {
		return err
	}
	header := make([]any, len(cols))
	for i, c := range cols {
		header[i] = c
	}
	if err := sw.SetRow("A1", header); err != nil {
		return fmt.Errorf("xlsx header: %w", err)
	}

	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range ptrs {
		ptrs[i] = &vals[i]
	}
	r := 1
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		r++
		cell, err := excelize.CoordinatesToCellName(1, r)
		if err != nil {
			return err
		}
		for i, v := range vals {
			vals[i] = toCell(v)
		}
		if err := sw.SetRow(cell, vals); err != nil {
			return fmt.Errorf("xlsx row %d: %w", r, err)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := sw.Flush(); err != nil {
		return fmt.Errorf("xlsx flush: %w", err)
	}
	if err := f.SaveAs(path); err != nil {
		return fmt.Errorf("xlsx save: %w", err)
	}
	return nil
}
