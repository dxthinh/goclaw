//go:build sqlite || sqliteonly

package sqlitestore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Lượt auto-inject không chèn gì không được tính vào số liệu retrieval.
func TestAggregateRetrievalIgnoresAutoInjectWithoutInjection(t *testing.T) {
	db, tenantID, agentID := newAgentUpdateTestFixture(t)
	metrics := NewSQLiteEvolutionMetricsStore(db)
	ctx := sqliteTenantCtx(tenantID)
	for _, value := range []map[string]any{
		{"outcome": "injected", "injected": 2, "top_score": 0.9, "used_in_reply": true},
		{"outcome": "no_match", "injected": 0, "top_score": 0.0, "used_in_reply": false},
		{"outcome": "below_threshold", "injected": 0, "top_score": 0.5, "used_in_reply": false},
	} {
		raw, _ := json.Marshal(value)
		if err := metrics.RecordMetric(ctx, store.EvolutionMetric{
			ID: uuid.New(), TenantID: tenantID, AgentID: agentID,
			MetricType: store.MetricRetrieval, MetricKey: "auto_inject", Value: raw,
		}); err != nil {
			t.Fatal(err)
		}
	}
	aggs, err := metrics.AggregateRetrievalMetrics(ctx, agentID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(aggs) != 1 || aggs[0].QueryCount != 1 || aggs[0].AvgScore != 0.9 {
		t.Fatalf("aggregate = %+v, muốn 1 lượt có điểm 0.9", aggs)
	}
}
