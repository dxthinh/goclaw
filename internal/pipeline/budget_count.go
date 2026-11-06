package pipeline

import (
	"log/slog"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

// PruneStage budgets with the request guard's BudgetCounter so both stages agree;
// TokenCounter falls back to a chars/2 heuristic for unregistered models.
func budgetMessageTokens(deps *PipelineDeps, model string, msgs []providers.Message) int {
	if deps.BudgetCounter != nil {
		n, err := deps.BudgetCounter.CountMessages(msgs)
		if err == nil {
			return n
		}
		slog.Warn("budget_counter.count_failed", "target", "messages", "error", err)
	}
	if deps.TokenCounter == nil {
		return 0
	}
	return deps.TokenCounter.CountMessages(model, msgs)
}

func budgetOverheadTokens(deps *PipelineDeps, model string, system providers.Message, tools []providers.ToolDefinition) (int, bool) {
	if deps.BudgetCounter != nil {
		msgTokens, err := deps.BudgetCounter.CountMessages([]providers.Message{system})
		if err == nil {
			var toolTokens int
			if toolTokens, err = deps.BudgetCounter.CountToolSchemas(tools); err == nil {
				return msgTokens + toolTokens, true
			}
		}
		slog.Warn("budget_counter.count_failed", "target", "overhead", "error", err)
	}
	if deps.TokenCounter == nil {
		return 0, false
	}
	return deps.TokenCounter.CountMessages(model, []providers.Message{system}) +
		deps.TokenCounter.CountToolSchemas(model, tools), true
}

// PromptShape là cơ cấu token của prompt ở cuối một run. Chỉ chứa số đếm, không chứa
// nội dung hội thoại.
type PromptShape struct {
	SystemTokens     int
	ToolSchemaTokens int
	HistoryTokens    int
	ToolResultTokens int
	MemoryTokens     int
	TotalEstimated   int
	Provider         string
	Model            string
}

// computePromptShape đếm token theo từng phần. overheadTokens là system + tool schema,
// đã trừ memory; phần tool schema là overhead trừ system. messages là history + pending
// không chứa memory. Đếm toàn bộ history tốn CPU nên chỉ gọi từ goroutine nền.
func computePromptShape(deps *PipelineDeps, model string, system providers.Message, messages []providers.Message, overheadTokens, memoryTokens int) PromptShape {
	var conversation, toolResults []providers.Message
	for _, msg := range messages {
		if msg.Role == "tool" {
			toolResults = append(toolResults, msg)
		} else {
			conversation = append(conversation, msg)
		}
	}
	shape := PromptShape{
		SystemTokens:     budgetMessageTokens(deps, model, []providers.Message{system}),
		HistoryTokens:    budgetMessageTokens(deps, model, conversation),
		ToolResultTokens: budgetMessageTokens(deps, model, toolResults),
		MemoryTokens:     memoryTokens,
		Model:            model,
	}
	shape.ToolSchemaTokens = max(overheadTokens-shape.SystemTokens, 0)
	shape.TotalEstimated = shape.SystemTokens + shape.ToolSchemaTokens + shape.HistoryTokens + shape.ToolResultTokens + shape.MemoryTokens
	return shape
}
