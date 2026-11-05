package pipeline

import (
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

const bufferTestMemory = "MEM"

func turnBuffer(history ...providers.Message) *MessageBuffer {
	mb := NewMessageBuffer(providers.Message{Role: "system", Content: "sys"})
	mb.SetHistory(history)
	mb.MarkCurrentTurn()
	return mb
}

func memoryIndexes(msgs []providers.Message) []int {
	var out []int
	for i, m := range msgs {
		if len(m.Content) >= len(bufferTestMemory) && m.Content[:len(bufferTestMemory)] == bufferTestMemory {
			out = append(out, i)
		}
	}
	return out
}

func wantMemoryAt(t *testing.T, mb *MessageBuffer, want int) {
	t.Helper()
	got := memoryIndexes(mb.ForRequest(bufferTestMemory))
	if len(got) != 1 || got[0] != want {
		t.Errorf("memory at %v, want [%d]", got, want)
	}
}

func TestForRequest_DoesNotMutateBufferAndKeepsMedia(t *testing.T) {
	t.Parallel()
	images := []providers.ImageContent{{MimeType: "image/png", Data: "abc"}}
	mb := turnBuffer(providers.Message{Role: "user", Content: "question", Images: images})

	req := mb.ForRequest(bufferTestMemory)
	if req[1].Content != bufferTestMemory+"\n\nquestion" {
		t.Errorf("request content = %q", req[1].Content)
	}
	if len(req[1].Images) != 1 {
		t.Errorf("images lost: %+v", req[1])
	}
	if got := mb.History()[0].Content; got != "question" {
		t.Errorf("history mutated: %q", got)
	}
	if got := mb.All()[1].Content; got != "question" {
		t.Errorf("All() must stay free of memory, got %q", got)
	}
}

func TestForRequest_EmptyUserContentGetsOnlyMemory(t *testing.T) {
	t.Parallel()
	mb := turnBuffer(providers.Message{Role: "user", Images: []providers.ImageContent{{Data: "abc"}}})
	if got := mb.ForRequest(bufferTestMemory)[1].Content; got != bufferTestMemory {
		t.Errorf("content = %q, want only memory", got)
	}
}

func TestForRequest_NoMemoryOrUnmarkedReturnsPlainMessages(t *testing.T) {
	t.Parallel()
	mb := NewMessageBuffer(providers.Message{Role: "system", Content: "sys"})
	mb.SetHistory([]providers.Message{{Role: "user", Content: "question"}})
	if got := mb.ForRequest(bufferTestMemory)[1].Content; got != "question" {
		t.Errorf("unmarked buffer content = %q", got)
	}
	mb.MarkCurrentTurn()
	if got := mb.ForRequest("")[1].Content; got != "question" {
		t.Errorf("empty memory content = %q", got)
	}
}

func TestMarkCurrentTurn_NoUserMessage(t *testing.T) {
	t.Parallel()
	mb := NewMessageBuffer(providers.Message{Role: "system"})
	mb.SetHistory([]providers.Message{{Role: "assistant", Content: "hi"}})
	if mb.MarkCurrentTurn() {
		t.Error("MarkCurrentTurn = true without any user message")
	}
}

// Tool loop: pending được đẩy vào history và có thêm tin user muộn, memory vẫn đứng yên.
func TestForRequest_PositionStableAcrossFlush(t *testing.T) {
	t.Parallel()
	mb := turnBuffer(
		providers.Message{Role: "user", Content: "old"},
		providers.Message{Role: "assistant", Content: "reply"},
		providers.Message{Role: "user", Content: "current"},
	)
	wantMemoryAt(t, mb, 3)

	mb.AppendPending(providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "1"}}})
	mb.AppendPending(providers.Message{Role: "tool", ToolCallID: "1", Content: "result"})
	mb.AppendPending(providers.Message{Role: "user", Content: "current"}) // người dùng gửi lại đúng câu đó
	wantMemoryAt(t, mb, 3)

	mb.FlushPending()
	wantMemoryAt(t, mb, 3)
}

func TestForRequest_RelocatesWhenHistoryShifts(t *testing.T) {
	t.Parallel()
	mb := turnBuffer(
		providers.Message{Role: "tool", ToolCallID: "orphan", Content: "orphan"},
		providers.Message{Role: "user", Content: "current"},
	)
	wantMemoryAt(t, mb, 2)

	mb.SetHistory([]providers.Message{{Role: "user", Content: "current"}})
	wantMemoryAt(t, mb, 1)
}

func TestForRequest_RelocatesAfterCompactionKeepsTurn(t *testing.T) {
	t.Parallel()
	mb := turnBuffer(
		providers.Message{Role: "user", Content: "old"},
		providers.Message{Role: "assistant", Content: "reply"},
		providers.Message{Role: "user", Content: "current"},
	)
	mb.ReplaceHistory([]providers.Message{
		{Role: "user", Content: "[Summary of earlier conversation]"},
		{Role: "user", Content: "current"},
	})
	wantMemoryAt(t, mb, 2)
}

// sanitizeHistory gộp hai tin user liền nhau thành một.
func TestForRequest_RelocatesAfterSanitizeMerge(t *testing.T) {
	t.Parallel()
	mb := turnBuffer(providers.Message{Role: "assistant", Content: "reply"}, providers.Message{Role: "user", Content: "current"})
	mb.SetHistory([]providers.Message{
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "current\n\n[System] hint"},
		{Role: "assistant", Content: "later"},
	})
	wantMemoryAt(t, mb, 2)

	// Sau khi bám vào tin đã gộp, các lần thay history sau vẫn nhận ra nó.
	mb.SetHistory([]providers.Message{{Role: "user", Content: "current\n\n[System] hint"}})
	wantMemoryAt(t, mb, 1)
}

// Tin hiện tại đã bị nén: memory bám vào bản tóm tắt ở đầu, không chen vào giữa cặp
// tool_use/tool_result còn lại.
func TestForRequest_FallsBackToFirstUserWhenTurnCompactedAway(t *testing.T) {
	t.Parallel()
	mb := turnBuffer(providers.Message{Role: "user", Content: "current"})
	mb.AppendPending(providers.Message{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "1"}}})
	mb.FlushPending()

	mb.ReplaceHistory([]providers.Message{
		{Role: "user", Content: "[Summary of earlier conversation]"},
		{Role: "assistant", ToolCalls: []providers.ToolCall{{ID: "2"}}},
		{Role: "tool", ToolCallID: "2", Content: "result"},
		{Role: "user", Content: "[System] wrap up"},
	})
	wantMemoryAt(t, mb, 1)

	mb.AppendPending(providers.Message{Role: "assistant", Content: "next"})
	mb.FlushPending()
	wantMemoryAt(t, mb, 1)
}

func TestForRequest_DropsMemoryWhenNoUserMessageRemains(t *testing.T) {
	t.Parallel()
	mb := turnBuffer(providers.Message{Role: "user", Content: "current"})
	mb.SetHistory([]providers.Message{{Role: "assistant", Content: "reply"}})
	if got := memoryIndexes(mb.ForRequest(bufferTestMemory)); len(got) != 0 {
		t.Errorf("memory at %v, want none", got)
	}
}
