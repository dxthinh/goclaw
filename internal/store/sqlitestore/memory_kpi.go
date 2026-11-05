//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// sqliteKPITimeLayout khớp định dạng mặc định strftime của cột created_at, để so sánh chuỗi
// với mốc thời gian cho kết quả đúng thứ tự.
const sqliteKPITimeLayout = "2006-01-02T15:04:05.000Z"

// SQLiteMemoryKPIStore triển khai store.MemoryKPIStore trên SQLite.
type SQLiteMemoryKPIStore struct {
	db *sql.DB
}

// NewSQLiteMemoryKPIStore tạo store KPI memory trên SQLite.
func NewSQLiteMemoryKPIStore(db *sql.DB) *SQLiteMemoryKPIStore {
	return &SQLiteMemoryKPIStore{db: db}
}

func (s *SQLiteMemoryKPIStore) InsertEvalResult(ctx context.Context, r store.MemoryEvalResult) error {
	tenantID := store.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		return store.ErrMemoryKPITenantRequired
	}
	if r.ID == uuid.Nil {
		r.ID = store.GenNewID()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	var traceID any
	if r.TraceID != nil {
		traceID = r.TraceID.String()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO memory_eval_results
		 (id, tenant_id, agent_id, channel, has_episodic, has_document, has_knowledge_graph,
		  item_count, relevant_items, used, reason, provider, model, input_tokens, output_tokens,
		  trace_id, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID.String(), tenantID.String(), r.AgentID.String(), r.Channel, r.HasEpisodic, r.HasDocument, r.HasKnowledgeGraph,
		r.ItemCount, r.RelevantItems, r.Used, r.Reason, r.Provider, r.Model, r.InputTokens, r.OutputTokens,
		traceID, r.CreatedAt.UTC().Format(sqliteKPITimeLayout))
	return err
}

func (s *SQLiteMemoryKPIStore) CountEvalsSince(ctx context.Context, since time.Time) (int, error) {
	tenantID := store.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		return 0, store.ErrMemoryKPITenantRequired
	}
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_eval_results WHERE tenant_id = ? AND created_at >= ?`,
		tenantID.String(), since.UTC().Format(sqliteKPITimeLayout)).Scan(&count)
	return count, err
}

func (s *SQLiteMemoryKPIStore) MissingRollupDays(ctx context.Context, days []time.Time) ([]time.Time, error) {
	if len(days) == 0 {
		return nil, nil
	}
	from, to := store.MemoryKPIDayRange(days)
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT bucket_date FROM memory_kpi_daily
		 WHERE bucket_date >= ? AND bucket_date <= ?
		   AND metric NOT IN (`+store.MemoryKPIGaugeMetricList+`)`,
		store.MemoryKPIDayKey(from), store.MemoryKPIDayKey(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	present := make(map[string]bool, len(days))
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			return nil, err
		}
		present[day] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return store.MemoryKPIDaysExcept(days, present), nil
}

func (s *SQLiteMemoryKPIStore) PruneRawMetrics(ctx context.Context, before store.MemoryKPIPruneCutoff) (int64, error) {
	metrics, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_evolution_metrics
		 WHERE created_at < ?
		   AND (metric_type = 'prompt' OR (metric_type = 'retrieval' AND metric_key = 'auto_inject'))`,
		before.Metrics.UTC().Format(sqliteKPITimeLayout))
	if err != nil {
		return 0, fmt.Errorf("prune metrics: %w", err)
	}
	evals, err := s.db.ExecContext(ctx, `DELETE FROM memory_eval_results WHERE created_at < ?`,
		before.EvalResults.UTC().Format(sqliteKPITimeLayout))
	if err != nil {
		return 0, fmt.Errorf("prune eval results: %w", err)
	}
	metricRows, err := metrics.RowsAffected()
	if err != nil {
		return 0, err
	}
	evalRows, err := evals.RowsAffected()
	if err != nil {
		return 0, err
	}
	return metricRows + evalRows, nil
}

func (s *SQLiteMemoryKPIStore) QueryDaily(ctx context.Context, agentID *uuid.UUID, from, to time.Time) ([]store.MemoryKPIDaily, error) {
	tenantID := store.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		return nil, store.ErrMemoryKPITenantRequired
	}
	query := `SELECT tenant_id, bucket_date, agent_id, metric, dimension, value, updated_at
		FROM memory_kpi_daily
		WHERE tenant_id = ? AND bucket_date >= ? AND bucket_date <= ?`
	args := []any{tenantID.String(), store.MemoryKPIDayKey(from), store.MemoryKPIDayKey(to)}
	if agentID != nil {
		query += ` AND agent_id = ?`
		args = append(args, agentID.String())
	}
	query += ` ORDER BY bucket_date, agent_id, metric, dimension`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []store.MemoryKPIDaily
	for rows.Next() {
		var d store.MemoryKPIDaily
		var tenant, agent, bucket string
		var updatedAt sqliteTime
		if err := rows.Scan(&tenant, &bucket, &agent, &d.Metric, &d.Dimension, &d.Value, &updatedAt); err != nil {
			return nil, err
		}
		if d.TenantID, err = uuid.Parse(tenant); err != nil {
			return nil, fmt.Errorf("parse tenant_id: %w", err)
		}
		if d.AgentID, err = uuid.Parse(agent); err != nil {
			return nil, fmt.Errorf("parse agent_id: %w", err)
		}
		if d.BucketDate, err = time.Parse(time.DateOnly, bucket); err != nil {
			return nil, fmt.Errorf("parse bucket_date: %w", err)
		}
		d.UpdatedAt = updatedAt.Time
		result = append(result, d)
	}
	return result, rows.Err()
}
