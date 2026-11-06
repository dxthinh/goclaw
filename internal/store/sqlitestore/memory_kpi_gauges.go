//go:build sqlite || sqliteonly

package sqlitestore

import "github.com/nextlevelbuilder/goclaw/internal/store"

// sqliteKPIGaugeStatements đọc trạng thái hiện tại nên chỉ có nghĩa với ngày đang chạy; tham
// số duy nhất là ?1 (ngày).
func sqliteKPIGaugeStatements() []string {
	insert := func(with string, metrics ...[2]string) string {
		return store.MemoryKPIInsertSQL(with, sqliteKPIDayExpr, sqliteKPINowExpr, metrics...)
	}
	return []string{
		// episodic_stale đếm session cần tóm tắt lại: đã có thêm từ 10 tin nhắn so với bản tóm tắt
		// phủ xa nhất. Tính theo session để dòng tóm tắt cũ không làm tăng số.
		insert(`WITH latest AS (
			SELECT tenant_id, agent_id, session_key, MAX(turn_count) AS turn_count
			FROM episodic_summaries
			WHERE source_type = 'session'
			GROUP BY 1, 2, 3
		), stale AS (
			SELECT l.tenant_id, l.agent_id, COUNT(*) AS n
			FROM latest l
			JOIN sessions s ON s.tenant_id = l.tenant_id AND s.session_key = l.session_key
			WHERE CASE WHEN json_valid(s.messages) THEN json_array_length(s.messages) ELSE 0 END >= l.turn_count + 10
			GROUP BY 1, 2
		), counts AS (
			SELECT tenant_id, agent_id, COUNT(*) AS total,
			       SUM(CASE WHEN promoted_at IS NOT NULL THEN 1 ELSE 0 END) AS promoted
			FROM episodic_summaries
			GROUP BY 1, 2
		), agg AS (
			SELECT c.tenant_id, c.agent_id, '' AS dimension, c.total, c.promoted, COALESCE(st.n, 0) AS stale
			FROM counts c
			LEFT JOIN stale st ON st.tenant_id = c.tenant_id AND st.agent_id = c.agent_id
		)`,
			[2]string{"episodic_total", "total"}, [2]string{"episodic_promoted", "promoted"},
			[2]string{"episodic_stale", "stale"}),

		insert(`WITH agg AS (
			SELECT tenant_id, agent_id, '' AS dimension, COUNT(*) AS n
			FROM kg_entities WHERE valid_until IS NULL GROUP BY 1, 2, 3
		)`, [2]string{"kg_entities", "n"}),

		insert(`WITH agg AS (
			SELECT tenant_id, agent_id, '' AS dimension, COUNT(*) AS n
			FROM kg_relations WHERE valid_until IS NULL GROUP BY 1, 2, 3
		)`, [2]string{"kg_relations", "n"}),

		insert(`WITH agg AS (
			SELECT tenant_id, agent_id, '' AS dimension, COUNT(*) AS docs,
			       SUM(CASE WHEN substr(path, 1, 17) = '_system/dreaming/' THEN 1 ELSE 0 END) AS dreaming
			FROM memory_documents GROUP BY 1, 2, 3
		)`, [2]string{"memory_documents", "docs"}, [2]string{"dreaming_documents", "dreaming"}),
	}
}
