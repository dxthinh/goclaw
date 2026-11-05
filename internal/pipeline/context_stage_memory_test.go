package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

const memoryTestSystem = "you are a helpful agent"

func memoryContextDeps(section string) *PipelineDeps {
	return &PipelineDeps{
		TokenCounter: finalRequestBudgetCounter{},
		BuildMessages: func(_ context.Context, input *RunInput, _ []providers.Message, _ string) ([]providers.Message, error) {
			return []providers.Message{
				{Role: "system", Content: memoryTestSystem},
				{Role: "user", Content: "earlier"},
				{Role: "assistant", Content: "reply"},
				{Role: "user", Content: input.Message},
			}, nil
		},
		AutoInject: func(context.Context, string, string, string) (string, error) { return section, nil },
	}
}

func memoryContextState(t *testing.T, deps *PipelineDeps) *RunState {
	t.Helper()
	state := defaultState()
	state.Input.Message = "current question"
	if err := NewContextStage(deps).Execute(context.Background(), state); err != nil {
		t.Fatalf("ContextStage: %v", err)
	}
	return state
}

func TestContextStage_SystemPromptStableWhenMemoryChanges(t *testing.T) {
	t.Parallel()
	first := memoryContextState(t, memoryContextDeps("MEMORY-A"))
	second := memoryContextState(t, memoryContextDeps("MEMORY-B"))
	none := memoryContextState(t, memoryContextDeps(""))

	for name, state := range map[string]*RunState{"first": first, "second": second, "none": none} {
		if got := state.Messages.System().Content; got != memoryTestSystem {
			t.Errorf("%s: system = %q, want it unchanged by memory", name, got)
		}
		req := state.Messages.ForRequest(state.Context.MemorySection)
		if req[0].Content != memoryTestSystem {
			t.Errorf("%s: request system = %q, want it unchanged by memory", name, req[0].Content)
		}
	}

	req := first.Messages.ForRequest(first.Context.MemorySection)
	if got := req[len(req)-1].Content; got != "MEMORY-A\n\ncurrent question" {
		t.Errorf("current turn = %q, want memory right before the user text", got)
	}
	// Tiền tố trước lượt hiện tại phải giống hệt để cache prompt còn dùng được.
	other := second.Messages.ForRequest(second.Context.MemorySection)
	for i := 0; i < len(req)-1; i++ {
		if req[i].Content != other[i].Content {
			t.Errorf("message %d differs between runs: %q vs %q", i, req[i].Content, other[i].Content)
		}
	}
}

// Không tạo tin user riêng: provider Claude CLI và ACP chỉ đọc tin user cuối, nhiều API
// khác đòi xen kẽ vai.
func TestContextStage_MemoryDoesNotAddConsecutiveUserMessage(t *testing.T) {
	t.Parallel()
	state := memoryContextState(t, memoryContextDeps("MEMORY"))

	req := state.Messages.ForRequest(state.Context.MemorySection)
	if len(req) != state.Messages.TotalLen() {
		t.Fatalf("request has %d messages, want %d (no extra message)", len(req), state.Messages.TotalLen())
	}
	for i := 1; i < len(req); i++ {
		if req[i].Role == "user" && req[i-1].Role == "user" {
			t.Errorf("messages %d and %d are consecutive user messages", i-1, i)
		}
	}
}

func TestContextStage_MemoryCountedInOverhead(t *testing.T) {
	t.Parallel()
	section := strings.Repeat("m", 40)
	state := memoryContextState(t, memoryContextDeps(section))
	if state.Context.MemoryTokens != len(section) {
		t.Errorf("MemoryTokens = %d, want %d", state.Context.MemoryTokens, len(section))
	}
	if want := len(memoryTestSystem) + len(section); state.Context.OverheadTokens != want {
		t.Errorf("OverheadTokens = %d, want %d (system + memory)", state.Context.OverheadTokens, want)
	}
}

func TestContextStage_NoUserMessageSkipsMemory(t *testing.T) {
	t.Parallel()
	deps := memoryContextDeps("MEMORY")
	deps.BuildMessages = func(context.Context, *RunInput, []providers.Message, string) ([]providers.Message, error) {
		return []providers.Message{{Role: "system", Content: memoryTestSystem}}, nil
	}
	state := memoryContextState(t, deps)
	if state.Context.MemorySection != "" || state.Context.MemoryTokens != 0 {
		t.Errorf("memory should be skipped, got %q / %d", state.Context.MemorySection, state.Context.MemoryTokens)
	}
}

// Chạy trọn pipeline qua một vòng tool: memory phải ở cùng vị trí trong mọi request và
// không có mặt trong những gì được lưu xuống session.
func TestPipeline_MemoryStaysInPlaceAndIsNeverPersisted(t *testing.T) {
	t.Parallel()
	const section = "MEMORY-BLOCK"
	var requests [][]providers.Message
	var flushed []providers.Message
	calls := 0

	deps := *memoryContextDeps(section)
	deps.Config = PipelineConfig{MaxIterations: 10, CheckpointInterval: 1, ContextWindow: 100_000, MaxTokens: 100}
	deps.FlushMessages = func(_ context.Context, _ string, msgs []providers.Message) error {
		flushed = append(flushed, msgs...)
		return nil
	}
	deps.CallLLM = func(_ context.Context, _ *RunState, req providers.ChatRequest) (*providers.ChatResponse, error) {
		requests = append(requests, req.Messages)
		calls++
		if calls <= 3 {
			return &providers.ChatResponse{FinishReason: "tool_calls", ToolCalls: []providers.ToolCall{{ID: "c" + strings.Repeat("x", calls), Name: "read_file", Arguments: map[string]any{"path": "a"}}}}, nil
		}
		return &providers.ChatResponse{Content: "done", FinishReason: "stop"}, nil
	}
	deps.ExecuteToolCall = func(_ context.Context, _ *RunState, tc providers.ToolCall) ([]providers.Message, error) {
		return []providers.Message{{Role: "tool", ToolCallID: tc.ID, Content: "result"}}, nil
	}

	state := defaultState()
	state.Input.Message = "current question"
	if _, err := NewDefaultPipeline(deps).Run(context.Background(), state); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 4 {
		t.Fatalf("LLM calls = %d, want 4", calls)
	}

	const wantIdx = 3 // system, earlier, reply, lượt hiện tại
	for i, req := range requests {
		for j, msg := range req {
			if has := strings.Contains(msg.Content, section); has != (j == wantIdx) {
				t.Errorf("request %d message %d (%s) has memory = %v, want only at %d", i, j, msg.Role, has, wantIdx)
			}
		}
		if got := req[wantIdx].Content; got != section+"\n\ncurrent question" {
			t.Errorf("request %d current turn = %q", i, got)
		}
	}

	if len(flushed) == 0 {
		t.Fatal("expected messages flushed to the session")
	}
	for _, msg := range flushed {
		if strings.Contains(msg.Content, section) {
			t.Errorf("flushed message contains memory: %+v", msg)
		}
	}
	for _, msg := range state.Messages.History() {
		if strings.Contains(msg.Content, section) {
			t.Errorf("history contains memory: %+v", msg)
		}
	}
}
