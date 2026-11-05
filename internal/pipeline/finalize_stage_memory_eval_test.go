package pipeline

import (
	"context"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

func memoryEvalDeps(got *[]MemoryEvalRun) *PipelineDeps {
	return &PipelineDeps{
		TokenCounter:     finalRequestBudgetCounter{},
		IsSilentReply:    func(content string) bool { return content == "NO_REPLY" },
		SubmitMemoryEval: func(_ context.Context, run MemoryEvalRun) { *got = append(*got, run) },
	}
}

// Tin nhắn tool của run hiện tại gồm cả phần checkpoint đã chuyển sang history, nhưng không
// được lẫn kết quả của lượt trước.
func TestFinalizeStage_SubmitsCurrentRunToMemoryEval(t *testing.T) {
	var got []MemoryEvalRun
	deps := memoryEvalDeps(&got)
	deps.SkillPostscript = func(_ context.Context, content string, _ int) string { return content + " [postscript]" }
	state := defaultState()
	state.Input.Message = "câu hỏi"
	state.Context.MemorySection = "MEM"
	state.Observe.FinalContent = "câu trả lời"
	state.Messages.SetHistory([]providers.Message{
		{Role: "user", Content: "lượt trước"},
		{Role: "tool", Content: "kết quả cũ", ToolName: "memory_search"},
		{Role: "user", Content: "câu hỏi"},
		{Role: "assistant"},
		{Role: "tool", Content: "đã flush", ToolName: "memory_search"},
	})
	state.Messages.AppendPending(providers.Message{Role: "assistant"})
	state.Messages.AppendPending(providers.Message{Role: "tool", Content: "đang chờ", ToolName: "knowledge_graph_search", IsError: true})

	if err := NewFinalizeStage(deps).Execute(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("submits = %d, muốn 1", len(got))
	}
	run := got[0]
	if run.Question != "câu hỏi" || run.MemorySection != "MEM" {
		t.Fatalf("run sai: %+v", run)
	}
	if run.Answer != "câu trả lời" {
		t.Fatalf("answer = %q, không được chứa postscript", run.Answer)
	}
	if len(run.ToolMessages) != 2 || run.ToolMessages[0].Content != "đã flush" || run.ToolMessages[1].Content != "đang chờ" || !run.ToolMessages[1].IsError {
		t.Fatalf("tool messages sai: %+v", run.ToolMessages)
	}
}

func TestFinalizeStage_SkipsMemoryEval(t *testing.T) {
	cases := map[string]func(*RunState){
		"câu trả lời rỗng": func(s *RunState) { s.Observe.FinalContent = "" },
		"chỉ khoảng trắng": func(s *RunState) { s.Observe.FinalContent = " \n" },
		"NO_REPLY":         func(s *RunState) { s.Observe.FinalContent = "NO_REPLY" },
		"run bị dừng":      func(s *RunState) { s.StopReason = "budget" },
		"run bị hủy":       func(s *RunState) { s.ExitCode = AbortRun },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var got []MemoryEvalRun
			state := defaultState()
			state.Input.Message = "câu hỏi"
			state.Observe.FinalContent = "câu trả lời"
			mutate(state)
			if err := NewFinalizeStage(memoryEvalDeps(&got)).Execute(context.Background(), state); err != nil {
				t.Fatal(err)
			}
			if len(got) != 0 {
				t.Fatalf("không được lấy mẫu: %+v", got)
			}
		})
	}
}

// Lỗi của bộ chấm không được làm hỏng finalize.
func TestFinalizeStage_MemoryEvalPanicDoesNotBreakFinalize(t *testing.T) {
	deps := &PipelineDeps{
		TokenCounter:     finalRequestBudgetCounter{},
		SubmitMemoryEval: func(context.Context, MemoryEvalRun) { panic("boom") },
	}
	state := defaultState()
	state.Observe.FinalContent = "câu trả lời"
	if err := NewFinalizeStage(deps).Execute(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if state.Observe.FinalContent != "câu trả lời" {
		t.Fatalf("FinalContent = %q", state.Observe.FinalContent)
	}
}
