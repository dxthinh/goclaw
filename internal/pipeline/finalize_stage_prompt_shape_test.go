package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

type namedTestProvider struct{ providers.Provider }

func (namedTestProvider) Name() string { return "test-provider" }

func TestFinalizeStage_RecordsPromptShape(t *testing.T) {
	t.Parallel()
	got := make(chan PromptShape, 1)
	deps := &PipelineDeps{
		TokenCounter:      finalRequestBudgetCounter{},
		RecordPromptShape: func(_ context.Context, shape PromptShape) { got <- shape },
	}
	state := defaultState()
	state.Provider = namedTestProvider{}
	state.Think.LastResponse = &providers.ChatResponse{Content: "answer"}
	state.Messages.SetSystem(providers.Message{Role: "system", Content: "12345"})
	state.Messages.SetHistory([]providers.Message{
		{Role: "user", Content: "hello"}, // 5
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "1"}}},
		{Role: "tool", ToolCallID: "1", Content: "tool-result"}, // 11
	})
	state.Messages.AppendPending(providers.Message{Role: "assistant", Content: "pend"}) // 4
	state.Messages.AppendPending(providers.Message{Role: "tool", Content: "t2"})        // 2
	state.Context.OverheadTokens = 100
	attachTurnMemory(deps, state, "1234567") // 7 token, cộng vào overhead
	state.Observe.FinalContent = "the final answer is not part of the last request"

	if err := NewFinalizeStage(deps).Execute(context.Background(), state); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	select {
	case shape := <-got:
		want := PromptShape{
			SystemTokens:     5,
			ToolSchemaTokens: 95, // overhead 100 trừ system 5, memory không tính vào đây
			HistoryTokens:    9,
			ToolResultTokens: 13,
			MemoryTokens:     7,
			TotalEstimated:   129,
			Provider:         "test-provider",
			Model:            state.Model,
		}
		if shape != want {
			t.Errorf("shape = %+v, want %+v", shape, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompt shape was not recorded")
	}
}

func TestFinalizeStage_NoPromptShapeWithoutLLMCall(t *testing.T) {
	t.Parallel()
	got := make(chan PromptShape, 1)
	deps := &PipelineDeps{
		TokenCounter:      finalRequestBudgetCounter{},
		RecordPromptShape: func(_ context.Context, shape PromptShape) { got <- shape },
	}
	state := defaultState()
	state.Messages.SetHistory([]providers.Message{{Role: "user", Content: "hello"}})

	if err := NewFinalizeStage(deps).Execute(context.Background(), state); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	select {
	case shape := <-got:
		t.Errorf("unexpected shape: %+v", shape)
	case <-time.After(50 * time.Millisecond):
	}
}

// LastResponse bị xóa khi một vòng thử lại thất bại, nhưng run vẫn đã gọi LLM.
func TestRunCalledLLM(t *testing.T) {
	t.Parallel()
	state := defaultState()
	if runCalledLLM(state) {
		t.Error("runCalledLLM = true before any call")
	}
	state.Think.TotalUsage.PromptTokens = 10
	if !runCalledLLM(state) {
		t.Error("runCalledLLM = false with accumulated usage")
	}
	state = defaultState()
	state.Think.LastResponse = &providers.ChatResponse{}
	if !runCalledLLM(state) {
		t.Error("runCalledLLM = false with a response")
	}
}
