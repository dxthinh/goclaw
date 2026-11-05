package tracing

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMergeUsageDoesNotDoubleCountTraceAndSpanTokens(t *testing.T) {
	agentID := uuid.New()
	bucket := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	// Trace gốc dùng 51 token, trace con dùng 49; tool dùng thêm 10 token.
	traces := []traceAggregate{{
		AgentID: &agentID, Channel: "web", RequestCount: 1, ErrorCount: 1,
		UniqueUsers: 1, InputTokens: 110, OutputTokens: 20, TotalCost: 1.5,
		ToolCallCount: 2, AvgDurationMS: 1500,
	}}
	spans := []spanAggregate{
		{AgentID: &agentID, Channel: "web", Provider: "provider", Model: "model",
			LLMCallCount: 2, InputTokens: 100, OutputTokens: 15, TotalCost: 1.25},
		{AgentID: &agentID, Channel: "web", InputTokens: 10, OutputTokens: 5, TotalCost: 0.25},
	}
	rows := mergeTraceAndSpanRows(bucket, traces, spans, nil, nil)
	if len(rows) != 2 {
		t.Fatalf("số dòng = %d, muốn 2", len(rows))
	}
	var input, output int64
	var cost float64
	var requests, failures, users, tools, calls int
	for _, row := range rows {
		input += row.InputTokens
		output += row.OutputTokens
		cost += row.TotalCost
		requests += row.RequestCount
		failures += row.ErrorCount
		users += row.UniqueUsers
		tools += row.ToolCallCount
		calls += row.LLMCallCount
	}
	if input != 110 || output != 20 || cost != 1.5 {
		t.Fatalf("usage = (%d, %d, %v), muốn (110, 20, 1.5)", input, output, cost)
	}
	if requests != 1 || failures != 1 || users != 1 || tools != 2 || calls != 2 {
		t.Fatalf("số lượng = (%d, %d, %d, %d, %d), muốn (1, 1, 1, 2, 2)", requests, failures, users, tools, calls)
	}
}
