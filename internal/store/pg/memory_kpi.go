package pg

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// PGMemoryKPIStore triển khai store.MemoryKPIStore trên PostgreSQL.
type PGMemoryKPIStore struct {
	db *sql.DB
}

// NewPGMemoryKPIStore tạo store KPI memory trên PostgreSQL.
func NewPGMemoryKPIStore(db *sql.DB) *PGMemoryKPIStore {
	return &PGMemoryKPIStore{db: db}
}

func (s *PGMemoryKPIStore) InsertEvalResult(ctx context.Context, r store.MemoryEvalResult) error {
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
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO memory_eval_results
		 (id, tenant_id, agent_id, channel, has_episodic, has_document, has_knowledge_graph,
		  item_count, relevant_items, used, reason, provider, model, input_tokens, output_tokens,
		  trace_id, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		r.ID, tenantID, r.AgentID, r.Channel, r.HasEpisodic, r.HasDocument, r.HasKnowledgeGraph,
		r.ItemCount, r.RelevantItems, r.Used, r.Reason, r.Provider, r.Model, r.InputTokens, r.OutputTokens,
		r.TraceID, r.CreatedAt)
	return err
}

func (s *PGMemoryKPIStore) CountEvalsSince(ctx context.Context, since time.Time) (int, error) {
	tenantID := store.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		return 0, store.ErrMemoryKPITenantRequired
	}
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_eval_results WHERE tenant_id = $1 AND created_at >= $2`,
		tenantID, since).Scan(&count)
	return count, err
}

func (s *PGMemoryKPIStore) MissingRollupDays(ctx context.Context, days []time.Time) ([]time.Time, error) {
	if len(days) == 0 {
		return nil, nil
	}
	from, to := store.MemoryKPIDayRange(days)
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT bucket_date FROM memory_kpi_daily
		 WHERE bucket_date >= $1 AND bucket_date <= $2
		   AND metric NOT IN (`+store.MemoryKPIGaugeMetricList+`)`,
		from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	present := make(map[string]bool, len(days))
	for rows.Next() {
		var day time.Time
		if err := rows.Scan(&day); err != nil {
			return nil, err
		}
		present[day.Format(time.DateOnly)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return store.MemoryKPIDaysExcept(days, present), nil
}

func (s *PGMemoryKPIStore) PruneRawMetrics(ctx context.Context, before store.MemoryKPIPruneCutoff) (int64, error) {
	metrics, err := s.db.ExecContext(ctx,
		`DELETE FROM agent_evolution_metrics
		 WHERE created_at < $1
		   AND (metric_type = 'prompt' OR (metric_type = 'retrieval' AND metric_key = 'auto_inject'))`,
		before.Metrics)
	if err != nil {
		return 0, fmt.Errorf("prune metrics: %w", err)
	}
	evals, err := s.db.ExecContext(ctx, `DELETE FROM memory_eval_results WHERE created_at < $1`, before.EvalResults)
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

func (s *PGMemoryKPIStore) QueryDaily(ctx context.Context, agentID *uuid.UUID, from, to time.Time) ([]store.MemoryKPIDaily, error) {
	tenantID := store.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		return nil, store.ErrMemoryKPITenantRequired
	}
	query := `SELECT tenant_id, bucket_date, agent_id, metric, dimension, value, updated_at
		FROM memory_kpi_daily
		WHERE tenant_id = $1 AND bucket_date >= $2 AND bucket_date <= $3`
	args := []any{tenantID, store.MemoryKPIDay(from), store.MemoryKPIDay(to)}
	if agentID != nil {
		query += ` AND agent_id = $4`
		args = append(args, *agentID)
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
		if err := rows.Scan(&d.TenantID, &d.BucketDate, &d.AgentID, &d.Metric, &d.Dimension, &d.Value, &d.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}
