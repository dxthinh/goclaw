//go:build integration || sqliteonly

package integration

import (
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"
)

// snapshot đọc rollup một ngày của tenant thành map để so nguyên khối.
func (e *kpiEnv) snapshot(t *testing.T, day time.Time) map[string]float64 {
	t.Helper()
	rows, err := e.h.kpi.QueryDaily(e.ctx, nil, day, day)
	if err != nil {
		t.Fatalf("QueryDaily: %v", err)
	}
	got := make(map[string]float64, len(rows))
	for _, r := range rows {
		key := r.Metric + "|" + r.Dimension
		if r.AgentID == uuid.Nil {
			key = "zero:" + key
		}
		got[key] = r.Value
	}
	return got
}

func assertKPI(t *testing.T, got, want map[string]float64) {
	t.Helper()
	for key, value := range want {
		if have, ok := got[key]; !ok || have != value {
			t.Errorf("%s = %v (có: %v), muốn %v", key, have, ok, value)
		}
	}
	for key, value := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("dòng thừa %s = %v", key, value)
		}
	}
}

func withGauges(sets ...map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for _, set := range sets {
		maps.Copy(out, set)
	}
	return out
}

// runMemoryKPISuite chạy cùng một fixture tính tay trên cả hai dialect.
func runMemoryKPISuite(t *testing.T, h *kpiHarness) {
	refresh := func(t *testing.T, e *kpiEnv, day time.Time, gauges bool) {
		t.Helper()
		if err := h.kpi.RefreshDailyRollup(e.ctx, day, gauges); err != nil {
			t.Fatalf("RefreshDailyRollup: %v", err)
		}
	}

	t.Run("RollupMatchesHandComputedFixture", func(t *testing.T) {
		e := h.newKPIEnv(t)
		e.seedCumulative(t)
		e.seedGauges(t)
		refresh(t, e, kpiDay, true)
		assertKPI(t, e.snapshot(t, kpiDay), withGauges(wantCumulative(), wantGauges()))
	})

	t.Run("RollupSurvivesTraceDeletion", func(t *testing.T) {
		e := h.newKPIEnv(t)
		e.seedCumulative(t)
		refresh(t, e, kpiDay, false)
		e.exec(t, `DELETE FROM spans WHERE tenant_id = $1`, e.tenant)
		e.exec(t, `DELETE FROM traces WHERE tenant_id = $1`, e.tenant)
		// Worker chỉ đụng ngày khác, ngày đã có không bị tính lại.
		refresh(t, e, kpiDay.AddDate(0, 0, 3), false)
		assertKPI(t, e.snapshot(t, kpiDay), wantCumulative())
	})

	t.Run("RefreshIsIdempotent", func(t *testing.T) {
		e := h.newKPIEnv(t)
		e.seedCumulative(t)
		e.seedGauges(t)
		refresh(t, e, kpiDay, true)
		first := e.snapshot(t, kpiDay)
		refresh(t, e, kpiDay, true)
		refresh(t, e, kpiDay, true)
		if second := e.snapshot(t, kpiDay); !maps.Equal(first, second) {
			t.Fatalf("hai lần refresh khác nhau:\n%v\n%v", first, second)
		}
	})

	t.Run("GaugesChangeOnlyWithIncludeGauges", func(t *testing.T) {
		e := h.newKPIEnv(t)
		e.seedCumulative(t)
		e.seedGauges(t)
		refresh(t, e, kpiDay, true)

		e.document(t, "moi-them.md")
		e.metric(t, "tool", "knowledge_graph_search", `{"success":true}`, kpiDay.Add(11*time.Hour))
		refresh(t, e, kpiDay, false)
		got := e.snapshot(t, kpiDay)
		if got["memory_documents|"] != 3 {
			t.Fatalf("gauge đổi khi includeGauges=false: %v", got["memory_documents|"])
		}
		if got["retrieval_events|knowledge_graph_search:unknown"] != 2 {
			t.Fatalf("phần cộng dồn phải được tính lại: %v", got["retrieval_events|knowledge_graph_search:unknown"])
		}

		refresh(t, e, kpiDay, true)
		if got := e.snapshot(t, kpiDay); got["memory_documents|"] != 4 {
			t.Fatalf("gauge không đổi khi includeGauges=true: %v", got["memory_documents|"])
		}
	})

	t.Run("BackfillKeepsExistingDays", func(t *testing.T) {
		e := h.newKPIEnv(t)
		present := kpiDay.AddDate(0, 0, -300)
		gaugeOnly := present.AddDate(0, 0, 1)
		empty := present.AddDate(0, 0, 2)
		insert := func(day time.Time, metric string, value float64) {
			e.exec(t, `INSERT INTO memory_kpi_daily (tenant_id, bucket_date, agent_id, metric, dimension, value)
				VALUES ($1, $2, $3, $4, '', $5)`, e.tenant, h.bucket(day), e.agent, metric, value)
		}
		insert(present, "prompt_runs", 42)
		insert(gaugeOnly, "episodic_total", 7)

		missing, err := h.kpi.MissingRollupDays(e.ctx, []time.Time{present, gaugeOnly, empty})
		if err != nil {
			t.Fatalf("MissingRollupDays: %v", err)
		}
		if len(missing) != 2 || !missing[0].Equal(gaugeOnly) || !missing[1].Equal(empty) {
			t.Fatalf("missing = %v, muốn %v và %v", missing, gaugeOnly, empty)
		}
		for _, day := range missing {
			refresh(t, e, day, false)
		}
		if got := e.snapshot(t, present); got["prompt_runs|"] != 42 {
			t.Fatalf("ngày đã có bị ghi đè: %v", got)
		}
		if got := e.snapshot(t, gaugeOnly); got["episodic_total|"] != 7 {
			t.Fatalf("điền thiếu không được đổi gauge: %v", got)
		}
	})

	runMemoryKPIStoreCases(t, h, refresh)
	runMemoryKPIEvaluatorCases(t, h)
}
