package pipeline

import (
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

// MessageBuffer wraps the message list with append/replace semantics.
// Sequential pipeline guarantees only one stage writes at a time — no mutex needed.
type MessageBuffer struct {
	system  providers.Message   // system prompt (rebuilt by ContextStage)
	history []providers.Message // conversation history
	pending []providers.Message // new messages this iteration (flushed at checkpoint)

	// turnIndex là vị trí tin user của lượt hiện tại trong history, nơi memory được
	// gắn khi dựng request. -1 khi chưa đánh dấu hoặc không còn tin user nào.
	turnIndex int
	// turnContent là nội dung tại vị trí đó, dùng để tìm lại khi history bị dịch chuyển.
	turnContent string
}

// NewMessageBuffer creates a buffer with the given system message.
func NewMessageBuffer(system providers.Message) *MessageBuffer {
	return &MessageBuffer{system: system, turnIndex: -1}
}

// All returns system + history + pending as a single slice for LLM calls.
func (mb *MessageBuffer) All() []providers.Message {
	out := make([]providers.Message, 0, 1+len(mb.history)+len(mb.pending))
	out = append(out, mb.system)
	out = append(out, mb.history...)
	out = append(out, mb.pending...)
	return out
}

// ForRequest trả danh sách tin nhắn gửi provider: giống All() nhưng khối memory được
// đặt ngay trước nội dung tin user của lượt hiện tại, trên bản sao.
//
// Gộp vào cùng tin nhắn thay vì thêm một tin user riêng vì: provider Claude CLI và ACP
// chỉ đọc tin user cuối nên sẽ mất memory; sanitizeHistory coi hai tin user liền nhau
// là cần gộp vì nhiều API đòi xen kẽ vai. Vị trí cố định giữ nguyên tiền tố prompt qua
// các vòng lặp tool. History và pending không đổi nên memory không bị lưu vào session
// và không đi vào compaction.
func (mb *MessageBuffer) ForRequest(memory string) []providers.Message {
	out := mb.All()
	if memory == "" || mb.turnIndex < 0 || mb.turnIndex >= len(mb.history) {
		return out
	}
	msg := &out[mb.turnIndex+1] // out[0] là system
	if msg.Content == "" {
		msg.Content = memory
	} else {
		msg.Content = memory + "\n\n" + msg.Content
	}
	return out
}

// MarkCurrentTurn đánh dấu tin user cuối của history là tin của lượt hiện tại. Trả
// false khi history không có tin user nào.
func (mb *MessageBuffer) MarkCurrentTurn() bool {
	for i := len(mb.history) - 1; i >= 0; i-- {
		if mb.history[i].Role == "user" {
			mb.turnIndex, mb.turnContent = i, mb.history[i].Content
			return true
		}
	}
	mb.turnIndex, mb.turnContent = -1, ""
	return false
}

// relocateTurn tìm lại tin của lượt hiện tại sau khi history bị thay (prune, compaction).
func (mb *MessageBuffer) relocateTurn() {
	if mb.turnIndex < 0 {
		return
	}
	isTurn := func(m providers.Message) bool { return m.Role == "user" && m.Content == mb.turnContent }
	if mb.turnIndex < len(mb.history) && isTurn(mb.history[mb.turnIndex]) {
		return
	}
	for i := len(mb.history) - 1; i >= 0; i-- {
		if isTurn(mb.history[i]) {
			mb.turnIndex = i
			return
		}
	}
	// sanitizeHistory có thể đã gộp tin này với một tin user kề bên.
	if mb.turnContent != "" {
		for i := len(mb.history) - 1; i >= 0; i-- {
			if m := mb.history[i]; m.Role == "user" && strings.Contains(m.Content, mb.turnContent) {
				mb.turnIndex, mb.turnContent = i, m.Content
				return
			}
		}
	}
	// Tin đã bị nén vào bản tóm tắt: gắn vào tin user đầu tiên (chính bản tóm tắt
	// compaction), nơi không tách cặp tool_use/tool_result nào.
	for i, m := range mb.history {
		if m.Role == "user" {
			mb.turnIndex, mb.turnContent = i, m.Content
			return
		}
	}
	mb.turnIndex, mb.turnContent = -1, ""
}

// System returns the system message.
func (mb *MessageBuffer) System() providers.Message { return mb.system }

// SetSystem replaces the system message (ContextStage rebuilds it).
func (mb *MessageBuffer) SetSystem(msg providers.Message) { mb.system = msg }

// History returns conversation history (read-only view).
func (mb *MessageBuffer) History() []providers.Message { return mb.history }

// SetHistory replaces history (used when loading from session store).
func (mb *MessageBuffer) SetHistory(msgs []providers.Message) {
	mb.history = msgs
	mb.relocateTurn()
}

// AppendPending adds a new message to the pending buffer.
func (mb *MessageBuffer) AppendPending(msg providers.Message) {
	mb.pending = append(mb.pending, msg)
}

// Pending returns pending messages (read-only view).
func (mb *MessageBuffer) Pending() []providers.Message { return mb.pending }

// FlushPending moves pending messages to history and returns them.
func (mb *MessageBuffer) FlushPending() []providers.Message {
	flushed := mb.pending
	mb.history = append(mb.history, mb.pending...)
	mb.pending = nil
	return flushed
}

// ReplaceHistory replaces history after compaction.
func (mb *MessageBuffer) ReplaceHistory(msgs []providers.Message) {
	mb.history = msgs
	mb.pending = nil // compaction absorbs pending
	mb.relocateTurn()
}

// HistoryLen returns history count (excludes system + pending).
func (mb *MessageBuffer) HistoryLen() int { return len(mb.history) }

// TotalLen returns total message count including system.
func (mb *MessageBuffer) TotalLen() int {
	return 1 + len(mb.history) + len(mb.pending)
}
