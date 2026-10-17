package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
)

// import.go: 票06 读侧——外部 PG/MySQL 表拉进 DuckDB warehouse 成为实体表。
// 可插拔扩展点是 from 字符串前缀分发（与 export.go 的 dest 分发对称）:
//   - postgres://user:pass@host:port/db/表名 → pgx COPY TO STDOUT → CSV → read_csv 建表
//   - mysql://user:pass@host:port/db/表名    → database/sql 流式拉行 → CSV → read_csv 建表
//   - s3://...                               → v0.x（duckdb 扩展对 windows_amd64_mingw 404，同探针结论）
//
// 探针实证（2026-10-17）：duckdb postgres/mysql_scanner 扩展无 windows_amd64_mingw 二进制，
// ATTACH 路线不可行，故走 Go 侧拉取。CSV 中转与票05 sink 镜像对称。

// Import 将 from 指向的外部表拉进 warehouse，落为实体表 into。
func Import(db *sql.DB, from, into string) error {
	switch {
	case strings.HasPrefix(from, "postgres://"), strings.HasPrefix(from, "postgresql://"):
		return importFromPostgres(db, from, into)
	case strings.HasPrefix(from, "mysql://"):
		return importFromMySQL(db, from, into)
	default:
		return fmt.Errorf("unsupported from %q: 仅支持 postgres:// 与 mysql://", from)
	}
}

// splitTableRef: 从 <scheme>://user:pass@host[:port]/db/表名[?params] 中拆出连接串和表名。
func splitTableRef(ref string) (dsn, table string, err error) {
	u, err := url.Parse(ref)
	if err != nil {
		return "", "", err
	}
	table = path.Base(u.Path)
	if table == "" || table == "/" || table == "." {
		return "", "", fmt.Errorf("缺少表名: %q（格式 scheme://host/db/表名）", ref)
	}
	u.Path = path.Dir(u.Path)
	return u.String(), table, nil
}

// importCSVToWarehouse: CSV（含表头）→ warehouse 实体表。
func importCSVToWarehouse(db *sql.DB, csvPath, into string) error {
	q := fmt.Sprintf(`DROP TABLE IF EXISTS "%s"; CREATE TABLE "%s" AS SELECT * FROM read_csv('%s', header = true)`,
		into, into, filepath.ToSlash(csvPath))
	if _, err := db.Exec(q); err != nil {
		return fmt.Errorf("read_csv import: %w", err)
	}
	return nil
}

func importFromPostgres(db *sql.DB, from, into string) error {
	dsn, table, err := splitTableRef(from)
	if err != nil {
		return err
	}
	pg, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		return fmt.Errorf("pg connect: %w", err)
	}
	defer pg.Close(context.Background())

	tmp, err := os.CreateTemp("", "pduck-import-*.csv")
	if err != nil {
		return err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	f, err := os.OpenFile(tmp.Name(), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, err = pg.PgConn().CopyTo(context.Background(), f,
		fmt.Sprintf(`COPY "%s" TO STDOUT WITH (FORMAT csv, HEADER true)`, table))
	f.Close()
	if err != nil {
		return fmt.Errorf("pg copy to stdout: %w", err)
	}
	return importCSVToWarehouse(db, tmp.Name(), into)
}

func importFromMySQL(db *sql.DB, from, into string) error {
	dsn, table, err := splitTableRef(from)
	if err != nil {
		return err
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return err
	}
	port := u.Port()
	if port == "" {
		port = "3306"
	}
	cfg := mysql.NewConfig()
	cfg.User = u.User.Username()
	cfg.Passwd, _ = u.User.Password()
	cfg.Net = "tcp"
	cfg.Addr = u.Hostname() + ":" + port
	cfg.DBName = path.Base(u.Path)

	src, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return fmt.Errorf("mysql open: %w", err)
	}
	defer src.Close()

	rows, err := src.Query("SELECT * FROM `" + table + "`")
	if err != nil {
		return fmt.Errorf("mysql query: %w", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp("", "pduck-import-*.csv")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := csv.NewWriter(tmp)

	// ponytail: NULL 与空串在 CSV 里同为空字段；要求精确区分时换 NULL 标记 + read_csv(nullstr)
	raw := make([]sql.RawBytes, len(cols))
	ptrs := make([]any, len(cols))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	if err := w.Write(cols); err != nil {
		return err
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		rec := make([]string, len(cols))
		for i, v := range raw {
			rec[i] = string(v)
		}
		if err := w.Write(rec); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}
	tmp.Close()

	return importCSVToWarehouse(db, tmp.Name(), into)
}
