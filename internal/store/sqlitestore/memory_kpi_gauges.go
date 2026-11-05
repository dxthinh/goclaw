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
		insert(`WITH lens AS (
			SELECT s.tenant_id, s.session_key,
			       CASE WHEN json_valid(s.messages) THEN json_array_length(s.messages) ELSE 0 END AS n
			FROM sessions s
			WHERE EXISTS (SELECT 1 FROM episodic_summaries e
			              WHERE e.tenant_id = s.tenant_id AND e.session_key = s.session_key AND e.source_type = 'session')
		), agg AS (
			SELECT e.tenant_id, e.agent_id, '' AS dimension, COUNT(*) AS total,
			       SUM(CASE WHEN e.promoted_at IS NOT NULL THEN 1 ELSE 0 END) AS promoted,
			       SUM(CASE WHEN e.source_type = 'session' AND l.n >= e.turn_count + 10 THEN 1 ELSE 0 END) AS stale
			FROM episodic_summaries e
			LEFT JOIN lens l ON l.tenant_id = e.tenant_id AND l.session_key = e.session_key
			GROUP BY 1, 2, 3
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
