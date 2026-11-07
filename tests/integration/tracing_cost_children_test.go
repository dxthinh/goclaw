//go:build integration || sqliteonly

package integration

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func checkChildTraceCosts(t *testing.T, ts store.TracingStore, ctx context.Context, agentID uuid.UUID) {
	t.Helper()
	now := time.Now().UTC()
	parent := uuid.New()
	child := uuid.New()
	// A different child session reproduces delegation; a same-session grandchild
	// ensures recursive selection never counts a trace twice.
	for i, id := range []uuid.UUID{parent, child, uuid.New()} {
		trace := &store.TraceData{ID: id, AgentID: &agentID, SessionKey: "cost-parent",
			Status: store.TraceStatusCompleted, StartTime: now, CreatedAt: now}
		if i == 1 {
			trace.ParentTraceID = &parent
			trace.SessionKey = "cost-delegate"
		}
		if i == 2 {
			trace.ParentTraceID = &child
		}
		if err := ts.CreateTrace(ctx, trace); err != nil {
			t.Fatal(err)
		}
		// Update from real spans: the parent does not roll up child costs.
		span := &store.SpanData{ID: uuid.New(), TraceID: id, TenantID: store.TenantIDFromContext(ctx),
			SpanType: store.SpanTypeLLMCall, Status: store.SpanStatusCompleted,
			Level: store.SpanLevelDefault, StartTime: now, CreatedAt: now, InputTokens: 10}
		if err := ts.CreateSpan(ctx, span); err != nil {
			t.Fatal(err)
		}
		if err := ts.UpdateSpan(ctx, span.ID, map[string]any{"total_cost": float64(i + 1)}); err != nil {
			t.Fatal(err)
		}
		if err := ts.BatchUpdateTraceAggregates(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	reader := ts.(interface {
		GetSessionCosts(context.Context, []string) (map[string]float64, error)
	})
	costs, err := reader.GetSessionCosts(ctx, []string{"cost-parent", "cost-delegate", "cost-parent", "missing"})
	if err != nil || costs["cost-parent"] != 6 || costs["cost-delegate"] != 5 || len(costs) != 2 {
		t.Fatalf("session costs=%v err=%v, want parent=6 delegate=5", costs, err)
	}
	cost, err := ts.GetMonthlyAgentCost(ctx, agentID, now.Year(), now.Month())
	if err != nil || math.Abs(cost-6) > 1e-9 {
		t.Fatalf("monthly cost=%v err=%v, want 6", cost, err)
	}
	for _, opts := range []store.CostSummaryOpts{
		{AgentID: &agentID},
		{SessionKey: "cost-parent", AgentID: &agentID},
	} {
		rows, err := ts.GetCostSummary(ctx, opts)
		if err != nil || len(rows) != 1 || rows[0].TotalCost != 6 || rows[0].TraceCount != 3 || rows[0].TotalInputTokens != 30 {
			t.Fatalf("summary=%+v err=%v", rows, err)
		}
	}
}
