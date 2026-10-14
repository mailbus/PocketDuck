-- Ground truth 查询（票03）：每 Borough 每小时段平均车费
-- 数据源① 远端 Parquet（本地缓存 data/yellow_tripdata_2025-01.parquet，原始 URL 见下）
--   https://d37ci6vzurychx.cloudfront.net/trip-data/yellow_tripdata_2025-01.parquet
-- 数据源② 本地 SQLite 维表 testdata/zones.sqlite（TLC taxi zone lookup）
SELECT
  z.Borough,
  CAST(extract(hour FROM t.tpep_pickup_datetime) AS INTEGER) AS pickup_hour,
  round(avg(t.fare_amount), 2) AS avg_fare
FROM read_parquet('data/yellow_tripdata_2025-01.parquet') t
JOIN sqlite_scan('testdata/zones.sqlite', 'zones') z
  ON t.PULocationID = z.LocationID
WHERE z.Borough != 'Unknown'
GROUP BY 1, 2
ORDER BY 1, 2
