package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

// Compaction chỉ nhận history, nên bản tóm tắt không bao giờ chứa memory; memory vẫn đi
// cùng request sau đó.
func TestThinkStage_CompactionDoesNotSeeMemory(t *testing.T) {
	t.Parallel()
	const section = "MEMORY-BLOCK"
	var compactionInput []providers.Message
	var sent []providers.Message

	deps := &PipelineDeps{
		TokenCounter: finalRequestBudgetCounter{},
		Config:       PipelineConfig{ContextWindow: 100, MaxTokens: 10, Compaction: &config.CompactionConfig{MaxRequestShare: 0.85}},
		CompactMessages: func(_ context.Context, msgs []providers.Message, _ string) ([]providers.Message, error) {
			compactionInput = msgs
			return []providers.Message{{Role: "user", Content: "summary"}, msgs[len(msgs)-1]}, nil
		},
		CallLLM: func(_ context.Context, _ *RunState, req providers.ChatRequest) (*providers.ChatResponse, error) {
			sent = req.Messages
			return &providers.ChatResponse{Content: "ok", FinishReason: "stop"}, nil
		},
	}
	state := defaultState()
	state.Messages.SetSystem(providers.Message{Role: "system", Content: "sys"})
	state.Messages.SetHistory([]providers.Message{
		{Role: "user", Content: strings.Repeat("old", 40)},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "current"},
	})
	attachTurnMemory(deps, state, section)

	if err := NewThinkStage(deps).Execute(context.Background(), state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(compactionInput) == 0 {
		t.Fatal("expected compaction to run")
	}
	for _, msg := range compactionInput {
		if strings.Contains(msg.Content, section) {
			t.Errorf("compaction input contains memory: %+v", msg)
		}
	}
	if got := sent[len(sent)-1].Content; got != section+"\n\ncurrent" {
		t.Errorf("current turn after compaction = %q", got)
	}
}

// Vượt ngân sách và không rút gọn được history thì bỏ memory: request gửi đi không còn
// memory, và phần token đó được trả về ngân sách history.
func TestThinkStage_GuardDropsMemoryWhenOverBudget(t *testing.T) {
	t.Parallel()
	section := strings.Repeat("m", 80)
	var sent []providers.Message
	deps := &PipelineDeps{
		TokenCounter: finalRequestBudgetCounter{},
		Config:       PipelineConfig{ContextWindow: 100, MaxTokens: 10, Compaction: &config.CompactionConfig{MaxRequestShare: 0.85}},
		CallLLM: func(_ context.Context, _ *RunState, req providers.ChatRequest) (*providers.ChatResponse, error) {
			sent = req.Messages
			return &providers.ChatResponse{Content: "ok", FinishReason: "stop"}, nil
		},
	}
	state := defaultState()
	state.Messages.SetSystem(providers.Message{Role: "system", Content: "sys"})
	state.Messages.SetHistory([]providers.Message{{Role: "user", Content: "short"}})
	state.Context.OverheadTokens = len("sys")
	attachTurnMemory(deps, state, section)
	if state.Context.OverheadTokens != len("sys")+len(section) {
		t.Fatalf("OverheadTokens = %d before guard", state.Context.OverheadTokens)
	}

	if err := NewThinkStage(deps).Execute(context.Background(), state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if sent == nil {
		t.Fatal("expected CallLLM after memory was dropped")
	}
	for _, msg := range sent {
		if strings.Contains(msg.Content, section) {
			t.Errorf("sent message still contains memory: %+v", msg)
		}
	}
	if sent[len(sent)-1].Content != "short" {
		t.Errorf("user message = %q, want it intact", sent[len(sent)-1].Content)
	}
	if state.Context.MemorySection != "" || state.Context.MemoryTokens != 0 {
		t.Errorf("memory state not cleared: %q / %d", state.Context.MemorySection, state.Context.MemoryTokens)
	}
	if state.Context.OverheadTokens != len("sys") {
		t.Errorf("OverheadTokens = %d, want %d after dropping memory", state.Context.OverheadTokens, len("sys"))
	}
}

// Bộ giới hạn cuối cùng bên agent từ chối request cũng phải bỏ được memory qua cùng đường.
func TestThinkStage_BudgetRejectionFallsBackToDroppingMemory(t *testing.T) {
	t.Parallel()
	const section = "MEMORY-BLOCK"
	deps := &PipelineDeps{
		TokenCounter: finalRequestBudgetCounter{},
		Config:       PipelineConfig{ContextWindow: 100_000, MaxTokens: 10},
		CallLLM: func(_ context.Context, _ *RunState, _ providers.ChatRequest) (*providers.ChatResponse, error) {
			return nil, budgetExceededTestError{}
		},
	}
	state := defaultState()
	state.Messages.SetSystem(providers.Message{Role: "system", Content: "sys"})
	state.Messages.SetHistory([]providers.Message{{Role: "user", Content: "short"}})
	attachTurnMemory(deps, state, section)

	if err := NewThinkStage(deps).Execute(context.Background(), state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if state.Context.MemorySection != "" {
		t.Errorf("MemorySection = %q, want dropped by the reduction chain", state.Context.MemorySection)
	}
}

type budgetExceededTestError struct{}

func (budgetExceededTestError) Error() string               { return "request budget exceeded" }
func (budgetExceededTestError) ContextBudgetExceeded() bool { return true }
