//go:build sqlite || sqliteonly

package tracing

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

func TestSnapshotAggregatesIncludeChildrenAndOnlyCountRootRequests(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	for _, query := range []string{
		`CREATE TABLE traces (id TEXT, parent_trace_id TEXT, agent_id TEXT, channel TEXT,
		 user_id TEXT, status TEXT, start_time TIMESTAMP, end_time TIMESTAMP, duration_ms INTEGER,
		 total_input_tokens INTEGER, total_output_tokens INTEGER, total_cost REAL, tool_call_count INTEGER)`,
		`CREATE TABLE spans (trace_id TEXT, span_type TEXT, provider TEXT, model TEXT,
		 input_tokens INTEGER, output_tokens INTEGER, total_cost REAL, metadata TEXT)`,
	} {
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	bucket := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	agentID := uuid.New()
	// Trung bình duration chỉ tính trace gốc đã có duration; trace con không làm lệch số này.
	fixtures := []struct {
		id, user, status string
		parent          any
		end             any
		duration        any
		input           int
	}{
		{"root", "user", "completed", nil, bucket.Add(2 * time.Second), nil, 51},
		{"child", "child-user", "error", "root", bucket.Add(20 * time.Second), nil, 49},
		{"finished", "user", "error", nil, bucket.Add(9 * time.Second), 4000, 0},
		{"running", "user", "running", nil, nil, nil, 0},
	}
	for _, f := range fixtures {
		if _, err := db.ExecContext(ctx, `INSERT INTO traces VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.id, f.parent, agentID.String(), "web", f.user, f.status, bucket, f.end, f.duration, f.input, 0, 0, 1); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []struct {
		trace, kind, provider, model string
		input                       int
	}{
		{"root", "llm_call", "provider", "model", 51},
		{"child", "llm_call", "provider", "model", 49},
		{"child", "tool_call", "", "", 10},
		{"root", "agent", "", "", 100},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO spans VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			f.trace, f.kind, f.provider, f.model, f.input, 0, 0, `{}`); err != nil {
			t.Fatal(err)
		}
	}
	traces, err := queryTraceAggregates(ctx, db, bucket, bucket.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 {
		t.Fatalf("số nhóm trace = %d, muốn 1", len(traces))
	}
	tr := traces[0]
	if tr.RequestCount != 3 || tr.ErrorCount != 1 || tr.UniqueUsers != 1 || tr.ToolCallCount != 4 || tr.InputTokens != 100 || tr.AvgDurationMS != 4000 {
		t.Fatalf("tổng trace không đúng: %+v", tr)
	}
	spans, err := querySpanAggregates(ctx, db, bucket, bucket.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	rows := mergeTraceAndSpanRows(bucket, traces, spans, nil, nil)
	var input int64
	var calls int
	for _, row := range rows {
		input += row.InputTokens
		calls += row.LLMCallCount
	}
	if input != 110 || calls != 2 {
		t.Fatalf("usage gồm trace con và tool = (%d, %d), muốn (110, 2)", input, calls)
	}
}
