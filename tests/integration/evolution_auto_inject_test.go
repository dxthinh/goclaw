//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

func TestAggregateRetrievalCountsEmptyAutoInjectSearches(t *testing.T) {
	db := testDB(t)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := store.WithTenantID(context.Background(), tenantID)
	metrics := pg.NewPGEvolutionMetricsStore(db)
	for _, value := range []map[string]any{
		{"outcome": "injected", "injected": 2, "top_score": 0.9, "has_results": true},
		{"outcome": "no_match", "injected": 0, "top_score": 0.0, "has_results": false},
		{"outcome": "below_threshold", "injected": 0, "top_score": 0.5, "has_results": true},
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
	if len(aggs) != 1 || aggs[0].QueryCount != 3 || math.Abs(aggs[0].UsageRate-2.0/3.0) > 1e-9 {
		t.Fatalf("aggregate = %+v, want all 3 searches with result rate 2/3", aggs)
	}
}

func TestAggregateRetrievalCountsEmptyMemorySearches(t *testing.T) {
	db := testDB(t)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := store.WithTenantID(context.Background(), tenantID)
	metrics := pg.NewPGEvolutionMetricsStore(db)
	for _, value := range []map[string]any{
		{"outcome": "matched", "used_in_reply": true},
		{"used_in_reply": true}, // dòng cũ không có outcome vẫn được tính
		{"outcome": "no_match", "used_in_reply": false},
		{"outcome": "error", "used_in_reply": false},
	} {
		raw, _ := json.Marshal(value)
		if err := metrics.RecordMetric(ctx, store.EvolutionMetric{
			ID: uuid.New(), TenantID: tenantID, AgentID: agentID,
			MetricType: store.MetricRetrieval, MetricKey: "memory_search", Value: raw,
		}); err != nil {
			t.Fatal(err)
		}
	}
	aggs, err := metrics.AggregateRetrievalMetrics(ctx, agentID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(aggs) != 1 || aggs[0].QueryCount != 3 || math.Abs(aggs[0].UsageRate-2.0/3.0) > 1e-9 {
		t.Fatalf("aggregate = %+v, want 3 searches with result rate 2/3", aggs)
	}
}
