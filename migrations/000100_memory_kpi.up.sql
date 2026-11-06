-- KPI hiệu quả memory: kết quả bộ chấm theo mẫu và rollup theo ngày UTC.
--
-- Cả hai bảng cố ý không có khóa ngoại tới traces và agents: trace bị xóa ở ngày thứ 7
-- còn số liệu phải tồn tại lâu hơn nhiều, kể cả khi agent nguồn đã bị xóa.

CREATE TABLE memory_eval_results (
    id                  UUID PRIMARY KEY,
    tenant_id           UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    agent_id            UUID NOT NULL,
    channel             VARCHAR(40) NOT NULL,
    has_episodic        BOOLEAN NOT NULL DEFAULT false,
    has_document        BOOLEAN NOT NULL DEFAULT false,
    has_knowledge_graph BOOLEAN NOT NULL DEFAULT false,
    item_count          INTEGER NOT NULL DEFAULT 0,
    relevant_items      INTEGER NOT NULL DEFAULT 0,
    used                BOOLEAN NOT NULL DEFAULT false,
    reason              TEXT NOT NULL DEFAULT '',
    provider            TEXT NOT NULL DEFAULT '',
    model               TEXT NOT NULL DEFAULT '',
    input_tokens        BIGINT NOT NULL DEFAULT 0,
    output_tokens       BIGINT NOT NULL DEFAULT 0,
    trace_id            UUID,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Đếm hạn mức ngày theo tenant.
CREATE INDEX idx_memory_eval_results_tenant_created ON memory_eval_results (tenant_id, created_at);
-- Rollup và dọn dữ liệu chạy xuyên tenant nên không dùng được index có tenant_id đứng đầu.
CREATE INDEX idx_memory_eval_results_created ON memory_eval_results (created_at);

CREATE TABLE memory_kpi_daily (
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    bucket_date DATE NOT NULL,
    agent_id    UUID NOT NULL,
    metric      TEXT NOT NULL,
    dimension   TEXT NOT NULL DEFAULT '',
    value       DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, bucket_date, agent_id, metric, dimension)
);

-- Refresh xóa và kiểm tra theo ngày, không theo tenant.
CREATE INDEX idx_memory_kpi_daily_bucket ON memory_kpi_daily (bucket_date);

-- Rollup đọc LLM nền xuyên tenant; các index sẵn có của usage_events đều bắt đầu bằng tenant_id.
CREATE INDEX idx_usage_events_llm_background_time ON usage_events (event_time)
    WHERE event_type = 'llm_background';

-- Usage của giờ đang chạy tính cả trace con nên không dùng được index một phần idx_traces_start_root.
CREATE INDEX idx_traces_start_time ON traces (start_time DESC);

-- Vòng khôi phục của collector tìm trace thiếu duration_ms mỗi 30 giây; index gần như rỗng.
CREATE INDEX idx_traces_missing_duration ON traces (start_time)
    WHERE end_time IS NOT NULL AND duration_ms IS NULL;

-- Bù duration_ms cho trace cũ đã kết thúc nhưng chưa được ghi thời lượng.
UPDATE traces
   SET duration_ms = LEAST(
           GREATEST(ROUND(EXTRACT(EPOCH FROM (end_time - start_time)) * 1000), 0),
           2147483647
       )::INTEGER
 WHERE end_time IS NOT NULL
   AND duration_ms IS NULL;
