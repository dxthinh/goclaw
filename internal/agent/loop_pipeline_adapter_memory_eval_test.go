package agent

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/memoryeval"
	"github.com/nextlevelbuilder/goclaw/internal/pipeline"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tracing"
)

func TestMakeSubmitMemoryEval_NilWhenNotWired(t *testing.T) {
	t.Parallel()
	if got := (&Loop{}).makeSubmitMemoryEval(); got != nil {
		t.Fatal("không nối bộ chấm thì callback phải nil")
	}
}

func TestMakeSubmitMemoryEval_BuildsCandidates(t *testing.T) {
	t.Parallel()
	var got []memoryeval.Candidate
	agentID, tenantID, traceID := uuid.New(), uuid.New(), uuid.New()
	l := &Loop{agentUUID: agentID, submitMemoryEval: func(c memoryeval.Candidate) { got = append(got, c) }}
	ctx := tracing.WithTraceID(store.WithTenantID(context.Background(), tenantID), traceID)

	l.makeSubmitMemoryEval()(ctx, pipeline.MemoryEvalRun{
		Question:      "hỏi",
		Answer:        "đáp",
		MemorySection: "- mục một",
		ToolMessages: []providers.Message{
			{Role: "tool", ToolName: "memory_search", Content: `{"results":[{"tier":"document","snippet":"đoạn"}]}`},
		},
	})

	if len(got) != 2 {
		t.Fatalf("candidates = %d, muốn 2", len(got))
	}
	for _, c := range got {
		if c.TenantID != tenantID || c.AgentID != agentID || c.TraceID != traceID {
			t.Fatalf("định danh sai: %+v", c)
		}
	}
	if got[0].Channel != store.MemoryEvalChannelAutoInject || got[1].Channel != store.MemoryEvalChannelMemorySearch {
		t.Fatalf("channel sai: %s, %s", got[0].Channel, got[1].Channel)
	}
}

func TestMakeSubmitMemoryEval_SkipsWithoutTenant(t *testing.T) {
	t.Parallel()
	called := false
	l := &Loop{submitMemoryEval: func(memoryeval.Candidate) { called = true }}
	l.makeSubmitMemoryEval()(context.Background(), pipeline.MemoryEvalRun{
		Question: "hỏi", Answer: "đáp", MemorySection: "- mục",
	})
	if called {
		t.Fatal("thiếu tenant thì không được gửi ứng viên")
	}
}
