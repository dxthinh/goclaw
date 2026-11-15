package tools

import (
	"fmt"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// RenderTaskContract sinh khối "## Task Contract" cho system prompt của lượt
// member. Văn bản tiếng Anh vì dành cho LLM. Trả chuỗi rỗng khi không có contract.
// Khối chỉ mô tả một task: task của lượt chạy hiện tại.
func RenderTaskContract(task *store.TeamTaskData, c *TaskContract) string {
	if task == nil || c == nil {
		return ""
	}
	version := c.Version
	if version < 1 {
		version = 1
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Task Contract — task #%d (v%d)\n", task.TaskNumber, version)
	sb.WriteString("This contract is the authoritative definition of your current task. If the task message or older conversation history disagrees with it, follow the contract.\n")
	if c.Objective != "" {
		sb.WriteString("Objective: " + c.Objective + "\n")
	}
	writeContractBullets(&sb, "Deliverables:", c.Deliverables)
	if len(c.Acceptance) > 0 {
		sb.WriteString("Acceptance criteria (report each one when you call complete):\n")
		for i, a := range c.Acceptance {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, a)
		}
	}
	writeContractBullets(&sb, "In scope:", c.ScopeIn)
	writeContractBullets(&sb, "Out of scope:", c.ScopeOut)
	writeContractBullets(&sb, "Inputs:", c.Inputs)
	if c.OnUnclear == TaskContractOnUnclearAssume {
		sb.WriteString("If unclear: make the most reasonable assumption, continue, and state every assumption in your result\n")
	} else {
		sb.WriteString("If unclear: ask the lead via team_tasks(action=\"comment\", type=\"blocker\", text=\"<your question>\") instead of guessing\n")
	}
	if len(c.Amendments) > 0 {
		sb.WriteString("Amendments:\n")
		for _, a := range c.Amendments {
			fmt.Fprintf(&sb, "- v%d (%s, %s, %s): %s\n", a.Version, a.Kind, a.By, a.At.UTC().Format("2006-01-02 15:04"), a.Text)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func writeContractBullets(sb *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	sb.WriteString(title + "\n")
	for _, it := range items {
		sb.WriteString("- " + it + "\n")
	}
}
