package main

import (
	"database/sql"
	"fmt"
	"strings"
)

// export.go: 票05 写侧流水线——DuckDB 查询结果写出。
// 可插拔扩展点是 dest 字符串（宪法: 依赖克制，sink 按前缀分发）:
//   - <path>.parquet  → DuckDB COPY TO parquet（parquet 扩展静态链接，无外部依赖）
//   - postgres://...  → 任务2.2: DuckDB COPY TO csv → pgx COPY FROM STDIN
//   - s3://...        → v0.x: httpfs 扩展接真对象存储

// Export 将 query 结果写出到 dest。
func Export(db *sql.DB, query, dest string) error {
	switch {
	case strings.HasSuffix(dest, ".parquet"):
		return exportParquet(db, query, dest)
	case strings.HasPrefix(dest, "postgres://"), strings.HasPrefix(dest, "postgresql://"):
		return exportPostgres(db, query, dest)
	default:
		return fmt.Errorf("unsupported dest %q: 仅支持 *.parquet 与 postgres://", dest)
	}
}

// exportPostgres: 任务2.2 实现（DuckDB COPY CSV → pgx COPY FROM STDIN）。
func exportPostgres(db *sql.DB, query, dsn string) error {
	return fmt.Errorf("postgres sink: 尚未实现（票05 任务2.2）")
}

// exportParquet: COPY 直接落 parquet 文件。
func exportParquet(db *sql.DB, query, path string) error {
	q := fmt.Sprintf("COPY (%s) TO '%s' (FORMAT parquet)", query, path)
	if _, err := db.Exec(q); err != nil {
		return fmt.Errorf("copy parquet: %w", err)
	}
	return nil
}
