//go:build integration || sqliteonly

package integration

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// runMemoryKPIStoreCases kiểm tra phần dọn dữ liệu và cách ly tenant của store.
func runMemoryKPIStoreCases(t *testing.T, h *kpiHarness, refresh func(*testing.T, *kpiEnv, time.Time, bool)) {
	t.Run("PruneRemovesOnlyExpiredRawData", func(t *testing.T) {
		e := h.newKPIEnv(t)
		now := kpiDay
		old, recent := now.AddDate(0, 0, -31), now.AddDate(0, 0, -29)
		e.metric(t, "prompt", "shape", `{}`, old)
		e.metric(t, "retrieval", "auto_inject", `{}`, old)
		e.metric(t, "retrieval", "memory_search", `{}`, old)
		e.metric(t, "tool", "knowledge_graph_search", `{}`, old)
		e.metric(t, "tool", "web_search", `{}`, old)
		e.metric(t, "prompt", "shape", `{}`, recent)
		for _, r := range []time.Time{now.AddDate(0, 0, -181), now.AddDate(0, 0, -179)} {
			if err := h.kpi.InsertEvalResult(e.ctx, store.MemoryEvalResult{AgentID: e.agent, Channel: store.MemoryEvalChannelAutoInject, CreatedAt: r}); err != nil {
				t.Fatalf("InsertEvalResult: %v", err)
			}
		}
		e.exec(t, `INSERT INTO memory_kpi_daily (tenant_id, bucket_date, agent_id, metric, dimension, value)
			VALUES ($1, $2, $3, 'prompt_runs', '', 1)`, e.tenant, h.bucket(now.AddDate(-2, 0, 0)), e.agent)

		deleted, err := h.kpi.PruneRawMetrics(e.ctx, store.MemoryKPIPruneCutoff{
			Metrics: now.AddDate(0, 0, -30), EvalResults: now.AddDate(0, 0, -180),
		})
		if err != nil {
			t.Fatalf("PruneRawMetrics: %v", err)
		}
		if deleted < 3 {
			t.Fatalf("deleted = %d, muốn ít nhất 3", deleted)
		}
		count := func(query string) int {
			var n int
			if err := h.db.QueryRow(query, e.tenant).Scan(&n); err != nil {
				t.Fatalf("%s: %v", query, err)
			}
			return n
		}
		if n := count(`SELECT COUNT(*) FROM agent_evolution_metrics WHERE tenant_id = $1`); n != 3 {
			t.Fatalf("metric còn %d, muốn 3 (memory_search cũ, tool cũ, prompt mới)", n)
		}
		if n := count(`SELECT COUNT(*) FROM memory_eval_results WHERE tenant_id = $1`); n != 1 {
			t.Fatalf("eval còn %d, muốn 1", n)
		}
		if n := count(`SELECT COUNT(*) FROM memory_kpi_daily WHERE tenant_id = $1`); n != 1 {
			t.Fatalf("rollup còn %d, muốn 1 vì không bao giờ bị dọn", n)
		}
	})

	t.Run("TenantIsolationAndFailClosed", func(t *testing.T) {
		e, other := h.newKPIEnv(t), h.newKPIEnv(t)
		e.seedCumulative(t)
		refresh(t, e, kpiDay, false)

		if got := other.snapshot(t, kpiDay); len(got) != 0 {
			t.Fatalf("tenant khác đọc được rollup: %v", got)
		}
		if rows, err := h.kpi.QueryDaily(other.ctx, &e.agent, kpiDay, kpiDay); err != nil || len(rows) != 0 {
			t.Fatalf("lọc theo agent của tenant khác: rows = %d, err = %v", len(rows), err)
		}
		if n, err := h.kpi.CountEvalsSince(other.ctx, kpiDay); err != nil || n != 0 {
			t.Fatalf("CountEvalsSince tenant khác = %d, err = %v", n, err)
		}
		if n, err := h.kpi.CountEvalsSince(e.ctx, kpiDay); err != nil || n != 4 {
			t.Fatalf("CountEvalsSince = %d, err = %v, muốn 4", n, err)
		}
		noTenant := t.Context()
		if _, err := h.kpi.QueryDaily(noTenant, nil, kpiDay, kpiDay); !errors.Is(err, store.ErrMemoryKPITenantRequired) {
			t.Fatalf("QueryDaily thiếu tenant: err = %v", err)
		}
		if _, err := h.kpi.CountEvalsSince(noTenant, kpiDay); !errors.Is(err, store.ErrMemoryKPITenantRequired) {
			t.Fatalf("CountEvalsSince thiếu tenant: err = %v", err)
		}
		if err := h.kpi.InsertEvalResult(noTenant, store.MemoryEvalResult{AgentID: e.agent}); !errors.Is(err, store.ErrMemoryKPITenantRequired) {
			t.Fatalf("InsertEvalResult thiếu tenant: err = %v", err)
		}

		rows, err := h.kpi.QueryDaily(e.ctx, &uuid.Nil, kpiDay, kpiDay)
		if err != nil {
			t.Fatalf("QueryDaily agent 0: %v", err)
		}
		if len(rows) != 3 {
			t.Fatalf("lọc agent UUID 0 có %d dòng, muốn 3", len(rows))
		}
	})
}
