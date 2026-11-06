-- Hoàn tác 000100. duration_ms đã bù cho trace cũ được giữ lại vì đó là giá trị đúng.
DROP INDEX IF EXISTS idx_traces_missing_duration;
DROP INDEX IF EXISTS idx_traces_start_time;
DROP INDEX IF EXISTS idx_usage_events_llm_background_time;
DROP TABLE IF EXISTS memory_kpi_daily;
DROP TABLE IF EXISTS memory_eval_results;
