package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Giới hạn kích thước của một contract.
const (
	taskContractMaxString     = 2000
	taskContractMaxItems      = 20
	taskContractMaxAmendments = 20
)

// Giá trị hợp lệ của on_unclear và checkpoint.
const (
	TaskContractOnUnclearAsk     = "ask"
	TaskContractOnUnclearAssume  = "assume_and_note"
	TaskContractCheckpointNone   = ""
	TaskContractCheckpointResult = "result"
)

// TaskContract là đặc tả có cấu trúc của một task: mục tiêu, sản phẩm và tiêu
// chí nghiệm thu. Lưu trong metadata["contract"] của task, không có cột riêng.
type TaskContract struct {
	Objective    string                  `json:"objective"`
	Deliverables []string                `json:"deliverables"`
	Acceptance   []string                `json:"acceptance"`
	ScopeIn      []string                `json:"scope_in,omitempty"`
	ScopeOut     []string                `json:"scope_out,omitempty"`
	Inputs       []string                `json:"inputs,omitempty"`
	OnUnclear    string                  `json:"on_unclear,omitempty"`
	Needs        []string                `json:"needs,omitempty"`
	Checkpoint   string                  `json:"checkpoint,omitempty"`
	Limits       *TaskContractLimits     `json:"limits,omitempty"`
	Version      int                     `json:"version,omitempty"`
	Amendments   []TaskContractAmendment `json:"amendments,omitempty"`
}

// TaskContractLimits giới hạn tài nguyên cho lượt chạy của task.
type TaskContractLimits struct {
	MaxIterations int `json:"max_iterations,omitempty"`
}

// TaskContractAmendment là một lần bổ sung contract sau khi tạo (câu trả lời,
// yêu cầu sửa, giao lại). Do hệ thống ghi, không nhận từ tham số tool.
type TaskContractAmendment struct {
	Version int       `json:"version"`
	Text    string    `json:"text"`
	By      string    `json:"by"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
}

// taskContractListFields là các trường kiểu mảng chuỗi. LLM hay gửi một chuỗi
// thay cho mảng một phần tử, nên ParseTaskContract chấp nhận cả hai.
var taskContractListFields = []string{"deliverables", "acceptance", "scope_in", "scope_out", "inputs", "needs"}

// ParseTaskContract đọc contract từ tham số tool hoặc từ metadata đã qua JSON
// (map[string]any, số là float64, mảng là []any). Cũng nhận chuỗi JSON.
// Trả nil, nil khi raw rỗng. Không kiểm tra trường bắt buộc; gọi Validate.
func ParseTaskContract(raw any) (*TaskContract, error) {
	switch v := raw.(type) {
	case nil:
		return nil, nil
	case *TaskContract:
		return v, nil
	case TaskContract:
		return &v, nil
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, nil
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			return nil, errors.New("contract must be an object")
		}
		raw = m
	case map[string]any:
	default:
		return nil, errors.New("contract must be an object")
	}
	m, _ := raw.(map[string]any)
	norm := make(map[string]any, len(m))
	for k, val := range m {
		norm[k] = val
	}
	for _, f := range taskContractListFields {
		if s, ok := norm[f].(string); ok {
			norm[f] = []any{s}
		}
	}
	b, err := json.Marshal(norm)
	if err != nil {
		return nil, fmt.Errorf("contract is not serializable: %w", err)
	}
	var c TaskContract
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("contract has a field of the wrong type: %w", err)
	}
	c.normalize()
	return &c, nil
}

// normalize bỏ khoảng trắng thừa và phần tử rỗng trong các mảng.
func (c *TaskContract) normalize() {
	c.Objective = strings.TrimSpace(c.Objective)
	c.OnUnclear = strings.TrimSpace(c.OnUnclear)
	c.Checkpoint = strings.TrimSpace(c.Checkpoint)
	for _, p := range []*[]string{&c.Deliverables, &c.Acceptance, &c.ScopeIn, &c.ScopeOut, &c.Inputs, &c.Needs} {
		out := (*p)[:0]
		for _, s := range *p {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			out = nil
		}
		*p = out
	}
}

// taskContractExample là ví dụ ngắn đi kèm lỗi để LLM sửa được ngay.
const taskContractExample = `Example: contract={"objective":"Summarize Q3 sales for the board","deliverables":["report.md in the team workspace"],"acceptance":["Covers all 4 regions","Every number cites its source file"]}`

// Validate kiểm tra trường bắt buộc, giá trị liệt kê và giới hạn kích thước.
// Liệt kê đủ mọi lỗi trong một lần, kèm một ví dụ.
func (c *TaskContract) Validate() error {
	if c == nil {
		return errors.New("contract is required. " + taskContractExample)
	}
	var missing, problems []string
	if c.Objective == "" {
		missing = append(missing, "objective")
	}
	if len(c.Deliverables) == 0 {
		missing = append(missing, "deliverables")
	}
	if len(c.Acceptance) == 0 {
		missing = append(missing, "acceptance")
	}
	switch c.OnUnclear {
	case "", TaskContractOnUnclearAsk, TaskContractOnUnclearAssume:
	default:
		problems = append(problems, fmt.Sprintf("on_unclear must be %q or %q", TaskContractOnUnclearAsk, TaskContractOnUnclearAssume))
	}
	switch c.Checkpoint {
	case TaskContractCheckpointNone, TaskContractCheckpointResult:
	default:
		problems = append(problems, fmt.Sprintf("checkpoint must be empty or %q", TaskContractCheckpointResult))
	}
	if c.Limits != nil && c.Limits.MaxIterations < 0 {
		problems = append(problems, "limits.max_iterations must not be negative")
	}
	if utf8.RuneCountInString(c.Objective) > taskContractMaxString {
		problems = append(problems, fmt.Sprintf("objective exceeds %d characters", taskContractMaxString))
	}
	lists := []struct {
		name  string
		items []string
	}{
		{"deliverables", c.Deliverables}, {"acceptance", c.Acceptance}, {"scope_in", c.ScopeIn},
		{"scope_out", c.ScopeOut}, {"inputs", c.Inputs}, {"needs", c.Needs},
	}
	for _, l := range lists {
		if len(l.items) > taskContractMaxItems {
			problems = append(problems, fmt.Sprintf("%s has more than %d items", l.name, taskContractMaxItems))
		}
		for _, s := range l.items {
			if utf8.RuneCountInString(s) > taskContractMaxString {
				problems = append(problems, fmt.Sprintf("an item of %s exceeds %d characters", l.name, taskContractMaxString))
				break
			}
		}
	}
	if len(c.Amendments) > taskContractMaxAmendments {
		problems = append(problems, fmt.Sprintf("more than %d amendments", taskContractMaxAmendments))
	}
	if len(missing) == 0 && len(problems) == 0 {
		return nil
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "contract is missing required fields: "+strings.Join(missing, ", "))
	}
	parts = append(parts, problems...)
	return errors.New(strings.Join(parts, "; ") + ". " + taskContractExample)
}

// TaskContractFromTask đọc contract trong metadata của task. Trả nil khi task
// không có contract hoặc contract không đọc được.
func TaskContractFromTask(task *store.TeamTaskData) *TaskContract {
	if task == nil || task.Metadata == nil {
		return nil
	}
	c, err := ParseTaskContract(task.Metadata[TaskMetaContract])
	if err != nil {
		return nil
	}
	return c
}

// ToMetadata đổi contract sang map[string]any để metadata có cùng hình dạng
// trước và sau khi qua JSON của store.
func (c *TaskContract) ToMetadata() map[string]any {
	b, _ := json.Marshal(c)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}
