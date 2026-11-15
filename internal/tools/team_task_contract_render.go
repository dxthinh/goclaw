package tools

import (
	"fmt"
	"regexp"
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
		sb.WriteString("Objective: " + contractLine(c.Objective) + "\n")
	}
	writeContractBullets(&sb, "Deliverables:", c.Deliverables)
	if len(c.Acceptance) > 0 {
		sb.WriteString("Acceptance criteria (report each one when you call complete):\n")
		for i, a := range c.Acceptance {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, contractLine(a))
		}
	}
	writeContractBullets(&sb, "In scope:", c.ScopeIn)
	writeContractBullets(&sb, "Out of scope:", c.ScopeOut)
	writeContractBullets(&sb, "Inputs:", c.Inputs)
	if c.OnUnclear == TaskContractOnUnclearAssume {
		sb.WriteString("If unclear: make the most reasonable assumption, continue, and state every assumption in your result\n")
	} else {
		sb.WriteString("If unclear: ask via team_tasks(action=\"clarify\", text=\"<your question>\") instead of guessing; the task waits for the answer\n")
	}
	if len(c.Amendments) > 0 {
		sb.WriteString("Amendments:\n")
		for _, a := range c.Amendments {
			fmt.Fprintf(&sb, "- v%d (%s, %s, %s): %s\n", a.Version, contractLine(a.Kind), contractLine(a.By), a.At.UTC().Format("2006-01-02 15:04"), contractLine(a.Text))
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
		sb.WriteString("- " + contractLine(it) + "\n")
	}
}

// extraContextTag khớp thẻ mở/đóng <extra_context> bao quanh khối contract
// trong system prompt.
var extraContextTag = regexp.MustCompile(`(?i)</?\s*extra_context`)

// contractLineBreaks gồm mọi ký tự có thể bị hiểu là xuống dòng: CRLF, LF, CR,
// \v, \f, NEL (U+0085), LINE SEPARATOR (U+2028), PARAGRAPH SEPARATOR (U+2029).
var contractLineBreaks = strings.NewReplacer(
	"\r\n", " ", "\n", " ", "\r", " ", "\v", " ", "\f", " ",
	"\u0085", " ", "\u2028", " ", "\u2029", " ",
)

// contractLine đưa một phần tử contract về đúng một dòng: xuống dòng thành
// khoảng trắng để nội dung không giả được tiêu đề hay mục mới, và vô hiệu
// thẻ <extra_context> để không đóng sớm khối chứa contract.
func contractLine(s string) string {
	s = contractLineBreaks.Replace(s)
	return extraContextTag.ReplaceAllString(s, "[extra_context")
}
