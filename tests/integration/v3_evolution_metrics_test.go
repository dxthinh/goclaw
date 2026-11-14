//go:build integration

package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

func TestV3EvolutionMetrics_RecordAndQuery(t *testing.T) {
	db := testDB(t)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ms := pg.NewPGEvolutionMetricsStore(db)

	// Record a tool metric.
	value, _ := json.Marshal(map[string]any{"success": true, "duration_ms": 42})
	metric := store.EvolutionMetric{
		ID:         uuid.New(),
		TenantID:   tenantID,
		AgentID:    agentID,
		SessionKey: "test-session",
		MetricType: store.MetricTool,
		MetricKey:  "exec",
		Value:      value,
	}
	if err := ms.RecordMetric(ctx, metric); err != nil {
		t.Fatalf("RecordMetric: %v", err)
	}

	// Query it back.
	results, err := ms.QueryMetrics(ctx, agentID, store.MetricTool, time.Now().Add(-1*time.Hour), 10)
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(results))
	}
	if results[0].MetricKey != "exec" {
		t.Errorf("expected MetricKey=exec, got %q", results[0].MetricKey)
	}
}

func TestV3EvolutionMetrics_AggregateTools(t *testing.T) {
	db := testDB(t)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ms := pg.NewPGEvolutionMetricsStore(db)

	// Record 3 tool calls: 2 success, 1 failure.
	for i, success := range []bool{true, true, false} {
		value, _ := json.Marshal(map[string]any{"success": success, "duration_ms": 100 + i*50})
		if err := ms.RecordMetric(ctx, store.EvolutionMetric{
			ID: uuid.New(), TenantID: tenantID, AgentID: agentID,
			SessionKey: "s1", MetricType: store.MetricTool, MetricKey: "web_search",
			Value: value,
		}); err != nil {
			t.Fatalf("RecordMetric #%d: %v", i, err)
		}
	}

	aggs, err := ms.AggregateToolMetrics(ctx, agentID, time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("AggregateToolMetrics: %v", err)
	}
	if len(aggs) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggs))
	}
	if aggs[0].CallCount != 3 {
		t.Errorf("expected 3 calls, got %d", aggs[0].CallCount)
	}
	// 2/3 success = ~0.667
	if aggs[0].SuccessRate < 0.6 || aggs[0].SuccessRate > 0.7 {
		t.Errorf("expected ~0.667 success rate, got %f", aggs[0].SuccessRate)
	}
}

func TestV3EvolutionMetrics_RetrievalResultPresence(t *testing.T) {
	db := testDB(t)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ms := pg.NewPGEvolutionMetricsStore(db)

	// New result flags take precedence, with compatibility for historical rows.
	for _, value := range []string{
		`{"has_results":true}`,
		`{"has_results":false,"used_in_reply":true}`,
		`{"result_count":2,"used_in_reply":false}`,
		`{"result_count":0,"used_in_reply":true}`,
		`{"used_in_reply":true}`,
		`{"used_in_reply":false}`,
		`{}`,
		`{"has_results":true,"result_count":0}`,
	} {
		if err := ms.RecordMetric(ctx, store.EvolutionMetric{
			ID: uuid.New(), TenantID: tenantID, AgentID: agentID,
			MetricType: store.MetricRetrieval, MetricKey: "memory_search",
			Value: json.RawMessage(value),
		}); err != nil {
			t.Fatalf("RecordMetric: %v", err)
		}
	}
	aggs, err := ms.AggregateRetrievalMetrics(ctx, agentID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("AggregateRetrievalMetrics: %v", err)
	}
	if len(aggs) != 1 || aggs[0].QueryCount != 8 || aggs[0].UsageRate != 0.5 {
		t.Fatalf("unexpected result presence aggregate: %+v", aggs)
	}
	otherTenant, _ := seedTenantAgent(t, db)
	aggs, err = ms.AggregateRetrievalMetrics(tenantCtx(otherTenant), agentID, time.Time{})
	if err != nil || len(aggs) != 0 {
		t.Fatalf("cross-tenant aggregate: %+v, %v", aggs, err)
	}
}
