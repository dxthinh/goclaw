//go:build sqlite || sqliteonly

package sqlitestore

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// duration_ms phải được tính khi trace đã có end_time, kể cả khi trace không có span nào.
func TestBatchUpdateTraceAggregatesWritesDuration(t *testing.T) {
	db, tenantID, agentID := newAgentUpdateTestFixture(t)
	tracing := NewSQLiteTracingStore(db)
	ctx := sqliteTenantCtx(tenantID)
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	traceID := uuid.New()
	if err := tracing.CreateTrace(ctx, &store.TraceData{
		ID: traceID, AgentID: &agentID, Status: store.TraceStatusRunning,
		StartTime: start, CreatedAt: start, Name: "duration",
	}); err != nil {
		t.Fatal(err)
	}

	if err := tracing.BatchUpdateTraceAggregates(ctx, traceID); err != nil {
		t.Fatal(err)
	}
	running, err := tracing.GetTrace(ctx, traceID)
	if err != nil {
		t.Fatal(err)
	}
	if running.DurationMS != 0 {
		t.Fatalf("trace đang chạy có duration_ms = %d, muốn 0", running.DurationMS)
	}

	const want = 2500
	if err := tracing.UpdateTrace(ctx, traceID, map[string]any{
		"status":   store.TraceStatusCompleted,
		"end_time": start.Add(want * time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}
	if err := tracing.BatchUpdateTraceAggregates(ctx, traceID); err != nil {
		t.Fatal(err)
	}
	finished, err := tracing.GetTrace(ctx, traceID)
	if err != nil {
		t.Fatal(err)
	}
	// julianday là số thực nên cho phép lệch 2 ms.
	if diff := finished.DurationMS - want; diff < -2 || diff > 2 {
		t.Fatalf("duration_ms = %d, muốn khoảng %d", finished.DurationMS, want)
	}
}

// Tiến trình chết sau FinishTrace nhưng trước lần flush tổng hợp thì trace có end_time mà không có
// duration_ms; vòng khôi phục phải bù lại.
func TestRecoverStaleWritesMissingTraceDuration(t *testing.T) {
	db, tenantID, agentID := newAgentUpdateTestFixture(t)
	tracing := NewSQLiteTracingStore(db)
	ctx := sqliteTenantCtx(tenantID)
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	traceID := uuid.New()
	if err := tracing.CreateTrace(ctx, &store.TraceData{
		ID: traceID, AgentID: &agentID, Status: store.TraceStatusRunning,
		StartTime: start, CreatedAt: start, Name: "duration",
	}); err != nil {
		t.Fatal(err)
	}
	const want = 2500
	if err := tracing.UpdateTrace(ctx, traceID, map[string]any{
		"status":   store.TraceStatusCompleted,
		"end_time": start.Add(want * time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tracing.RecoverStaleRunningTraces(ctx, time.Now().Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := tracing.GetTrace(ctx, traceID)
	if err != nil {
		t.Fatal(err)
	}
	// julianday là số thực nên cho phép lệch 2 ms.
	if diff := got.DurationMS - want; diff < -2 || diff > 2 {
		t.Fatalf("duration_ms = %d, muốn khoảng %d", got.DurationMS, want)
	}
}
