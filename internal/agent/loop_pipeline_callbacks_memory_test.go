package agent

import (
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

// Từ khóa nằm trong khối memory không được làm model bị buộc gọi tool thêm một lần.
func TestShouldRetryTaskMCPIgnoresMemoryBlock(t *testing.T) {
	req := func(content string) providers.ChatRequest {
		return providers.ChatRequest{
			Tools:    []providers.ToolDefinition{{Function: &providers.ToolFunctionSchema{Name: "mcp_bx24__search_tasks"}}},
			Messages: []providers.Message{{Role: "user", Content: content}},
		}
	}
	const block = "<memory_context>\n## Memory Context\n\n- task cũ của tuần trước\n</memory_context>\n\n"
	if shouldRetryTaskMCP(req(block + "xin chào")) {
		t.Fatal("từ khóa trong khối memory không được kích hoạt thử lại")
	}
	if !shouldRetryTaskMCP(req(block + "liệt kê task của tôi")) {
		t.Fatal("từ khóa trong tin nhắn người dùng vẫn phải kích hoạt thử lại")
	}
}
