//go:build sqlite || sqliteonly

package sqlitestore

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/memory"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestRetrievalMetricsResultPresence(t *testing.T) {
	db := newHookTestDB(t)
	tenantID, agentID := seedHookTenantAgent(t, db)
	ctx := sqliteTenantCtx(tenantID)
	metrics := NewSQLiteEvolutionMetricsStore(db)
	values := []string{
		`{"has_results":true,"top_score":0.8}`,
		`{"has_results":false,"used_in_reply":true,"top_score":0.4}`,
		`{"used_in_reply":true,"top_score":0.6}`,
		`{"used_in_reply":false}`,
		`{"result_count":2,"used_in_reply":false}`,
		`{"result_count":0,"used_in_reply":true}`,
		`{}`,
		`{"has_results":true,"result_count":0}`,
	}
	for _, value := range values {
		if err := metrics.RecordMetric(ctx, store.EvolutionMetric{
			ID: uuid.New(), AgentID: agentID, MetricType: store.MetricRetrieval,
			MetricKey: "memory_search", Value: json.RawMessage(value),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A different tenant must never enter this aggregate.
	otherTenant, otherAgent := seedHookTenantAgent(t, db)
	if err := metrics.RecordMetric(sqliteTenantCtx(otherTenant), store.EvolutionMetric{
		ID: uuid.New(), AgentID: otherAgent, MetricType: store.MetricRetrieval,
		MetricKey: "memory_search", Value: json.RawMessage(`{"has_results":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := metrics.AggregateRetrievalMetrics(ctx, agentID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].QueryCount != 8 || got[0].UsageRate != 0.5 || math.Abs(got[0].AvgScore-0.225) > 1e-9 {
		t.Fatalf("unexpected aggregate: %+v", got)
	}
	isolated, err := metrics.AggregateRetrievalMetrics(sqliteTenantCtx(otherTenant), agentID, time.Time{})
	if err != nil || len(isolated) != 0 {
		t.Fatalf("tenant leak: %+v, %v", isolated, err)
	}
}

func TestAutoInjectRecordsEmptySearch(t *testing.T) {
	testAutoInjectMetric(t, false)
}

func TestAutoInjectRecordsResultsWithoutInjection(t *testing.T) {
	testAutoInjectMetric(t, true)
}

func testAutoInjectMetric(t *testing.T, withResults bool) {
	t.Helper()
	db := newHookTestDB(t)
	tenantID, agentID := seedHookTenantAgent(t, db)
	ctx := sqliteTenantCtx(tenantID)
	metrics := NewSQLiteEvolutionMetricsStore(db)
	episodes := NewSQLiteEpisodicStore(db)
	query := "Recall favorite morning beverage"
	expectedMatches := 0
	if withResults {
		expectedMatches = 1
		if err := episodes.Create(ctx, &store.EpisodicSummary{
			TenantID: tenantID, AgentID: agentID, UserID: "reader",
			SessionKey: "session", Summary: query, L0Abstract: "Morning beverage is green tea.",
			SourceID: "beverage", SourceType: "manual",
		}); err != nil {
			t.Fatal(err)
		}
	}
	injector := memory.NewAutoInjector(episodes, metrics, nil)
	result, err := injector.Inject(ctx, memory.InjectParams{
		AgentID: agentID.String(), TenantID: tenantID.String(), UserID: "reader",
		UserMessage: query, MaxTokens: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.MatchCount != expectedMatches || result.Injected != 0 || result.Section != "" {
		t.Fatalf("unexpected budget-limited search: %+v", result)
	}
	// Metric writes are best effort and asynchronous; wait for their observable
	// completion before the fixture closes its database.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		rows, err := metrics.QueryMetrics(ctx, agentID, store.MetricRetrieval, time.Now().Add(-time.Hour), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) > 0 {
			var value map[string]any
			if err := json.Unmarshal(rows[0].Value, &value); err != nil {
				t.Fatal(err)
			}
			if value["has_results"] != withResults || value["result_count"] != float64(expectedMatches) || value["injected"] != float64(0) {
				t.Fatalf("incorrect retrieval metric: %s", rows[0].Value)
			}
			if _, exists := value["used_in_reply"]; exists {
				t.Fatal("metric still claims answer usage")
			}
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("retrieval metric was not recorded")
		case <-ticker.C:
		}
	}
}
