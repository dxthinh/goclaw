//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
	"github.com/nextlevelbuilder/goclaw/internal/tracing"
)

func TestPostgresSnapshotRepair(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("explicit disposable TEST_DATABASE_URL required")
	}
	db := testDB(t)
	tenant, agent := seedTenantAgent(t, db)
	ctx := store.WithTenantID(context.Background(), tenant)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)
	end := hour.Add(time.Hour)
	ts := pg.NewPGTracingStore(db)
	parent := uuid.New()
	for i, id := range []uuid.UUID{parent, uuid.New()} {
		tr := &store.TraceData{ID: id, AgentID: &agent, StartTime: hour, CreatedAt: hour, Status: store.TraceStatusCompleted}
		if i == 1 {
			tr.ParentTraceID = &parent
		}
		if err := ts.CreateTrace(ctx, tr); err != nil {
			t.Fatal(err)
		}
		sp := &store.SpanData{ID: uuid.New(), TraceID: id, TenantID: tenant, SpanType: store.SpanTypeLLMCall,
			StartTime: hour, CreatedAt: hour, Status: store.SpanStatusCompleted, Level: store.SpanLevelDefault,
			Provider: "test", Model: "test", InputTokens: 10}
		if err := ts.CreateSpan(ctx, sp); err != nil {
			t.Fatal(err)
		}
		if err := ts.UpdateSpan(ctx, sp.ID, map[string]any{"total_cost": 1}); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			toolSpan := &store.SpanData{
				ID: uuid.New(), TraceID: id, TenantID: tenant,
				SpanType: store.SpanTypeToolCall, StartTime: hour, CreatedAt: hour,
				Status: store.SpanStatusCompleted, Level: store.SpanLevelDefault,
				Provider: "vision", Model: "image", InputTokens: 7, OutputTokens: 3,
			}
			if err := ts.CreateSpan(ctx, toolSpan); err != nil {
				t.Fatal(err)
			}
			if err := ts.UpdateSpan(ctx, toolSpan.ID, map[string]any{"total_cost": 0.5}); err != nil {
				t.Fatal(err)
			}
		}
		if err := ts.BatchUpdateTraceAggregates(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	ss := pg.NewPGSnapshotStore(db)
	if err := ss.UpsertSnapshots(ctx, []store.UsageSnapshot{
		{BucketHour: hour, AgentID: &agent, RequestCount: 1, MemoryDocs: 42},
		{BucketHour: hour, AgentID: &agent, Provider: "test", Model: "test", LLMCallCount: 1, InputTokens: 10, TotalCost: 1},
	}); err != nil {
		t.Fatal(err)
	}
	check := func(want int) {
		t.Helper()
		var tokens, docs, calls int
		var cost float64
		if err := db.QueryRow(`SELECT input_tokens, total_cost, llm_call_count FROM usage_snapshots WHERE tenant_id=$1 AND bucket_hour=$2 AND provider='test'`, tenant, hour).Scan(&tokens, &cost, &calls); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT memory_docs FROM usage_snapshots WHERE tenant_id=$1 AND bucket_hour=$2 AND provider=''`, tenant, hour).Scan(&docs); err != nil {
			t.Fatal(err)
		}
		if tokens != want || docs != 42 || cost != float64(want)/10 || calls != want/10 {
			t.Fatalf("tokens=%d want=%d historical memory_docs=%d want=42 cost=%v calls=%d", tokens, want, docs, cost, calls)
		}
		var toolTokens, toolCalls int
		var toolCost float64
		err := db.QueryRow(`SELECT input_tokens, total_cost, llm_call_count FROM usage_snapshots WHERE tenant_id=$1 AND bucket_hour=$2 AND provider='vision'`, tenant, hour).Scan(&toolTokens, &toolCost, &toolCalls)
		if want == 10 {
			if !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("preview or failed backup changed tool snapshot: %v", err)
			}
		} else if err != nil || toolTokens != 7 || toolCost != 0.5 || toolCalls != 0 {
			t.Fatalf("tool snapshot: tokens=%d cost=%v llm_calls=%d err=%v", toolTokens, toolCost, toolCalls, err)
		}
	}
	if _, err := tracing.RepairSnapshots(ctx, db, hour, end, nil); err != nil {
		t.Fatal(err)
	}
	check(10)
	backupErr := errors.New("backup unavailable")
	if _, err := tracing.RepairSnapshots(ctx, db, hour, end, func(context.Context) error { return backupErr }); !errors.Is(err, backupErr) {
		t.Fatalf("backup failure: %v", err)
	}
	check(10)
	backedUp := false
	if _, err := tracing.RepairSnapshots(ctx, db, hour, end, func(context.Context) error {
		check(10)
		backedUp = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !backedUp {
		t.Fatal("write without backup callback")
	}
	check(20)
	if _, err := tracing.RepairSnapshots(ctx, db, hour, end, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	check(20)
	if _, err := db.Exec(`DELETE FROM spans WHERE trace_id=$1`, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := tracing.RepairSnapshots(ctx, db, hour, end, func(context.Context) error { return nil }); err == nil {
		t.Fatal("accepted pruned source")
	}
	check(20)
	// Even if trace rows disappeared completely, existing snapshot counts
	// prevent replacing a retained hour from an empty source.
	if _, err := db.Exec(`DELETE FROM spans WHERE trace_id IN (SELECT id FROM traces WHERE agent_id=$1)`, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM traces WHERE agent_id=$1`, agent); err != nil {
		t.Fatal(err)
	}
	if _, err := tracing.RepairSnapshots(ctx, db, hour, end, func(context.Context) error { return nil }); err == nil {
		t.Fatal("accepted wholly missing source")
	}
	check(20)
}
