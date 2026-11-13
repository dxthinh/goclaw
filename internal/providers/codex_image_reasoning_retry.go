package providers

import (
	"maps"
	"strings"
)

// isImageComplexityRejection nhận diện lỗi upstream trả về khi tool image_generation
// bị gọi trong một request có reasoning.effort: backend suy ra tham số "complexity"
// từ mức reasoning và model ảnh đang dùng không nhận tham số đó, ví dụ
// "Complexity 'high' is not supported for this image model.".
func isImageComplexityRejection(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "complexity") && strings.Contains(msg, "not supported for this image model")
}

// hasThinkingLevel cho biết request có gửi reasoning.effort hay không.
func hasThinkingLevel(req ChatRequest) bool {
	level, ok := req.Options[OptThinkingLevel].(string)
	return ok && level != "" && level != "off"
}

// withoutThinkingLevel trả về bản sao của request không còn reasoning.effort.
// Options được sao chép để không sửa map của nơi gọi.
func withoutThinkingLevel(req ChatRequest) ChatRequest {
	opts := maps.Clone(req.Options)
	delete(opts, OptThinkingLevel)
	req.Options = opts
	return req
}

// codexResultHasReplayUnsafeOutput cho biết lượt gọi đã sinh nội dung mà gọi lại sẽ
// bị lặp: chữ trả lời, tool call hoặc ảnh. Phần thinking không tính vì lượt gọi lại
// sẽ thay thế nó.
func codexResultHasReplayUnsafeOutput(result *ChatResponse) bool {
	if result == nil {
		return false
	}
	return result.Content != "" || len(result.ToolCalls) > 0 || len(result.Images) > 0
}
