package pg

import (
	"context"
	"fmt"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Tham số chung của mọi câu rollup: $1 là ngày, $2 và $3 là khoảng thời gian UTC của ngày đó.
const (
	pgKPIDayExpr  = "$1::date"
	pgKPINowExpr  = "NOW()"
	pgKPIZeroUUID = "'00000000-0000-0000-0000-000000000000'::uuid"
)

// pgJSONNum đọc số từ khóa JSON, trả 0 khi thiếu hoặc sai kiểu để một dòng hỏng không làm
// hỏng cả lần rollup. col và key là hằng trong code, không nhận dữ liệu ngoài.
func pgJSONNum(col, key string) string {
	return "CASE WHEN jsonb_typeof(" + col + "->'" + key + "') = 'number' THEN (" + col + "->>'" + key + "')::double precision ELSE 0 END"
}

// RefreshDailyRollup tính lại một ngày xuyên tenant. Câu DELETE và INSERT nằm trong cùng
// transaction nên người đọc không bao giờ thấy ngày đang được ghi dở.
func (s *PGMemoryKPIStore) RefreshDailyRollup(ctx context.Context, day time.Time, includeGauges bool) error {
	start := store.MemoryKPIDay(day)
	end := start.AddDate(0, 0, 1)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rollup tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // Rollback sau Commit là no-op.

	deleteMetrics := `metric NOT IN (` + store.MemoryKPIGaugeMetricList + `)`
	if includeGauges {
		deleteMetrics = `TRUE`
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_kpi_daily WHERE bucket_date = $1 AND `+deleteMetrics, start); err != nil {
		return fmt.Errorf("delete rollup day: %w", err)
	}
	for _, stmt := range pgKPICumulativeStatements() {
		if _, err := tx.ExecContext(ctx, stmt, start, start, end); err != nil {
			return fmt.Errorf("insert rollup: %w", err)
		}
	}
	if includeGauges {
		for _, stmt := range pgKPIGaugeStatements() {
			if _, err := tx.ExecContext(ctx, stmt, start); err != nil {
				return fmt.Errorf("insert rollup gauges: %w", err)
			}
		}
	}
	return tx.Commit()
}

func pgKPICumulativeStatements() []string {
	insert := func(with string, metrics ...[2]string) string {
		return store.MemoryKPIInsertSQL(with, pgKPIDayExpr, pgKPINowExpr, metrics...)
	}
	return []string{
		insert(`WITH agg AS (
			SELECT tenant_id, agent_id,
			       CASE WHEN metric_type = 'tool' THEN metric_key || ':called'
			            ELSE metric_key || ':' || COALESCE(NULLIF(value->>'outcome', ''), 'unknown') END AS dimension,
			       COUNT(*) AS events
			FROM agent_evolution_metrics
			WHERE created_at >= $2 AND created_at < $3
			  AND ((metric_type = 'retrieval' AND metric_key IN ('auto_inject', 'memory_search'))
			    OR (metric_type = 'tool' AND metric_key = 'knowledge_graph_search'))
			GROUP BY 1, 2, 3
		)`, [2]string{"retrieval_events", "events"}),

		// Một mẫu có thể thuộc nhiều tầng nên phép nối nhân dòng theo tầng là chủ ý.
		insert(`WITH layers(layer) AS (
			VALUES ('auto_inject'), ('memory_search'), ('knowledge_graph'), ('episodic'), ('document')
		), agg AS (
			SELECT r.tenant_id, r.agent_id, l.layer AS dimension,
			       COUNT(*) AS samples,
			       SUM(r.item_count) AS items,
			       SUM(r.relevant_items) AS relevant,
			       SUM(CASE WHEN r.used THEN 1 ELSE 0 END) AS used_count
			FROM memory_eval_results r
			JOIN layers l ON (l.layer = 'auto_inject' AND r.channel = 'auto_inject')
			              OR (l.layer = 'memory_search' AND r.channel = 'memory_search')
			              OR (l.layer = 'knowledge_graph' AND r.channel = 'knowledge_graph_search')
			              OR (l.layer = 'episodic' AND r.has_episodic)
			              OR (l.layer = 'document' AND r.has_document)
			WHERE r.created_at >= $2 AND r.created_at < $3
			GROUP BY 1, 2, 3
		)`,
			[2]string{"eval_samples", "samples"}, [2]string{"eval_items", "items"},
			[2]string{"eval_relevant_items", "relevant"}, [2]string{"eval_used", "used_count"}),

		insert(`WITH agg AS (
			SELECT tenant_id, agent_id, '' AS dimension, COUNT(*) AS runs,
			       SUM(`+pgJSONNum("value", "system_tokens")+`) AS system_tokens,
			       SUM(`+pgJSONNum("value", "tool_schema_tokens")+`) AS tool_schema_tokens,
			       SUM(`+pgJSONNum("value", "history_tokens")+`) AS history_tokens,
			       SUM(`+pgJSONNum("value", "tool_result_tokens")+`) AS tool_result_tokens,
			       SUM(`+pgJSONNum("value", "memory_tokens")+`) AS memory_tokens
			FROM agent_evolution_metrics
			WHERE metric_type = 'prompt' AND metric_key = 'shape'
			  AND created_at >= $2 AND created_at < $3
			GROUP BY 1, 2, 3
		)`,
			[2]string{"prompt_runs", "runs"}, [2]string{"prompt_system_tokens", "system_tokens"},
			[2]string{"prompt_tool_schema_tokens", "tool_schema_tokens"}, [2]string{"prompt_history_tokens", "history_tokens"},
			[2]string{"prompt_tool_result_tokens", "tool_result_tokens"}, [2]string{"prompt_memory_tokens", "memory_tokens"}),

		// Tính mọi trace gồm trace con. Mẫu số cache theo hợp đồng: provider gộp sẵn phần
		// cache vào input thì lấy input, còn lại cộng thêm phần đọc và ghi cache.
		insert(`WITH agg AS (
			SELECT t.tenant_id, COALESCE(t.agent_id, s.agent_id, `+pgKPIZeroUUID+`) AS agent_id,
			       COALESCE(s.provider, '') || '/' || COALESCE(s.model, '') AS dimension,
			       COUNT(*) AS calls,
			       SUM(COALESCE(s.input_tokens, 0)) AS input_tokens,
			       SUM(`+pgJSONNum("s.metadata", "cache_read_tokens")+`) AS cache_read,
			       SUM(CASE WHEN s.metadata->>'prompt_tokens_include_cached_segments' = 'true'
			                THEN COALESCE(s.input_tokens, 0)
			                ELSE COALESCE(s.input_tokens, 0) + `+pgJSONNum("s.metadata", "cache_read_tokens")+` + `+pgJSONNum("s.metadata", "cache_creation_tokens")+` END) AS denominator,
			       SUM(CASE WHEN s.metadata->'cache_read_tokens' IS NOT NULL
			                  OR s.metadata->'prompt_tokens_include_cached_segments' IS NOT NULL
			                THEN 1 ELSE 0 END) AS reporting
			FROM spans s
			JOIN traces t ON t.id = s.trace_id
			WHERE s.span_type = 'llm_call' AND s.created_at >= $2 AND s.created_at < $3
			GROUP BY 1, 2, 3
		)`,
			[2]string{"llm_calls", "calls"}, [2]string{"llm_input_tokens", "input_tokens"},
			[2]string{"llm_cache_read_tokens", "cache_read"}, [2]string{"llm_cache_denominator_tokens", "denominator"},
			[2]string{"llm_cache_reporting_calls", "reporting"}),

		insert(`WITH agg AS (
			SELECT tenant_id, COALESCE(agent_id, `+pgKPIZeroUUID+`) AS agent_id, resource_name AS dimension,
			       SUM(call_count) AS calls, SUM(input_tokens) AS input_tokens, SUM(output_tokens) AS output_tokens
			FROM usage_events
			WHERE event_type = 'llm_background' AND event_time >= $2 AND event_time < $3
			GROUP BY 1, 2, 3
		)`,
			[2]string{"bg_calls", "calls"}, [2]string{"bg_input_tokens", "input_tokens"},
			[2]string{"bg_output_tokens", "output_tokens"}),
	}
}
