//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

// duration_ms phải được tính khi trace đã có end_time, kể cả khi trace không có span nào.
func TestTraceDurationWrittenByAggregates(t *testing.T) {
	db := testDB(t)
	tenantID, _ := seedTenantAgent(t, db)
	ctx := store.WithTenantID(context.Background(), tenantID)
	tracing := pg.NewPGTracingStore(db)
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	traceID := uuid.New()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO traces (id, tenant_id, status, start_time) VALUES ($1, $2, $3, $4)`,
		traceID, tenantID, store.TraceStatusRunning, start); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM traces WHERE id = $1`, traceID)
	})

	readDuration := func() *int {
		t.Helper()
		var got *int
		if err := db.QueryRowContext(ctx, `SELECT duration_ms FROM traces WHERE id = $1`, traceID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	if err := tracing.BatchUpdateTraceAggregates(ctx, traceID); err != nil {
		t.Fatal(err)
	}
	if got := readDuration(); got != nil {
		t.Fatalf("trace đang chạy có duration_ms = %d, muốn NULL", *got)
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
	got := readDuration()
	if got == nil || *got != want {
		t.Fatalf("duration_ms = %v, muốn %d", got, want)
	}
}
