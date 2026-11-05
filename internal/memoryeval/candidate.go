// Package memoryeval chấm theo mẫu chất lượng memory được truy hồi và tổng hợp KPI theo ngày.
package memoryeval

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Tên tool sau khi đã chuẩn hóa theo registry.
const (
	toolMemorySearch         = "memory_search"
	toolKnowledgeGraphSearch = "knowledge_graph_search"
)

// Candidate là một mẫu chờ chấm. Chứa nội dung hội thoại nên chỉ tồn tại trong bộ nhớ,
// không bao giờ được ghi xuống store hay log.
type Candidate struct {
	TenantID          uuid.UUID
	AgentID           uuid.UUID
	TraceID           uuid.UUID
	Channel           string
	Question          string
	Items             []string
	HasEpisodic       bool
	HasDocument       bool
	HasKnowledgeGraph bool
	Answer            string
}

// ToolResult là kết quả một lần gọi tool trong run, tên đã chuẩn hóa theo registry.
type ToolResult struct {
	Name    string
	Content string
	IsError bool
}

// RunSnapshot là phần dữ liệu của một run đã kết thúc mà bộ chấm cần.
type RunSnapshot struct {
	TenantID      uuid.UUID
	AgentID       uuid.UUID
	TraceID       uuid.UUID
	Question      string
	Answer        string
	MemorySection string
	ToolResults   []ToolResult
}

// CandidatesFromRun dựng ứng viên từ một run, mỗi channel tối đa một ứng viên. Kết quả tool
// rỗng hoặc đọc không được thì bỏ qua vì không có gì để chấm. Khi một tool được gọi nhiều
// lần, lấy lần gần câu trả lời nhất vì đó là truy vấn đã được model tinh chỉnh.
func CandidatesFromRun(run RunSnapshot) []Candidate {
	question, answer := strings.TrimSpace(run.Question), strings.TrimSpace(run.Answer)
	if run.TenantID == uuid.Nil || question == "" || answer == "" {
		return nil
	}
	base := Candidate{
		TenantID: run.TenantID,
		AgentID:  run.AgentID,
		TraceID:  run.TraceID,
		Question: cutRunes(question, maxQuestionRunes),
		Answer:   cutRunes(answer, maxAnswerRunes),
	}

	var out []Candidate
	if items := memorySectionItems(run.MemorySection); len(items) > 0 {
		c := base
		c.Channel, c.Items, c.HasEpisodic = store.MemoryEvalChannelAutoInject, items, true
		out = append(out, c)
	}
	var haveSearch, haveGraph bool
	for i := len(run.ToolResults) - 1; i >= 0; i-- {
		result := run.ToolResults[i]
		if result.IsError {
			continue
		}
		switch {
		case result.Name == toolMemorySearch && !haveSearch:
			if c, ok := memorySearchCandidate(base, result.Content); ok {
				out, haveSearch = append(out, c), true
			}
		case result.Name == toolKnowledgeGraphSearch && !haveGraph:
			if items := knowledgeGraphItems(result.Content); len(items) > 0 {
				c := base
				c.Channel, c.Items, c.HasKnowledgeGraph = store.MemoryEvalChannelKnowledgeGraph, items, true
				out, haveGraph = append(out, c), true
			}
		}
	}
	return out
}

// memorySectionItems tách các mục của khối memory L0. Mỗi mục bắt đầu bằng "- "; phần mở đầu
// trước mục đầu tiên và thẻ đóng không phải dữ liệu.
func memorySectionItems(section string) []string {
	var items []string
	for _, line := range strings.Split(section, "\n") {
		switch {
		case strings.HasPrefix(line, "- "):
			items = append(items, strings.TrimSpace(line[2:]))
		case len(items) > 0 && strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "</memory_context>"):
			items[len(items)-1] += " " + strings.TrimSpace(line)
		}
	}
	return items
}

// memorySearchCandidate đọc JSON kết quả memory_search. Thông báo không có kết quả và JSON bị
// cắt khi prune đều không parse được, nên ứng viên bị bỏ thay vì chấm trên dữ liệu sai.
func memorySearchCandidate(base Candidate, content string) (Candidate, bool) {
	var output struct {
		Results []struct {
			Tier       string `json:"tier"`
			Path       string `json:"path"`
			Snippet    string `json:"snippet"`
			L0Abstract string `json:"l0_abstract"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(content), &output); err != nil || len(output.Results) == 0 {
		return Candidate{}, false
	}
	c := base
	c.Channel = store.MemoryEvalChannelMemorySearch
	for _, r := range output.Results {
		switch r.Tier {
		case "document":
			c.HasDocument = true
		case "episodic":
			c.HasEpisodic = true
		}
		text := r.Snippet
		if text == "" {
			text = r.L0Abstract
		}
		if text == "" {
			text = r.Path
		}
		c.Items = append(c.Items, text)
	}
	return c, true
}

// knowledgeGraphItems tách các mục từ văn bản của knowledge_graph_search. Thực thể bắt đầu
// bằng "- ", các dòng thụt lề sau đó thuộc về thực thể ấy; dạng kết nối trực tiếp chỉ có dòng
// thụt lề nên mỗi dòng là một mục. Câu trả lời "không tìm thấy" có thể kèm danh sách gợi ý
// không phải kết quả nên bị loại ngay từ đầu.
func knowledgeGraphItems(content string) []string {
	if strings.HasPrefix(content, "No ") || strings.HasPrefix(content, "Knowledge graph is ") {
		return nil
	}
	var items []string
	inEntity := false
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "- "):
			items, inEntity = append(items, strings.TrimSpace(line[2:])), true
		case !strings.HasPrefix(line, " ") || strings.TrimSpace(line) == "":
			continue
		case inEntity:
			items[len(items)-1] += " " + strings.TrimSpace(line)
		default:
			items = append(items, strings.TrimSpace(line))
		}
	}
	return items
}
