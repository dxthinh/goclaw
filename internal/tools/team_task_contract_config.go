package tools

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nextlevelbuilder/goclaw/internal/edition"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// TaskContractConfig bật Task Contract cho một team. Khi bật, mọi task trừ
// loại request và note phải kèm contract hợp lệ mới được giao cho member.
type TaskContractConfig struct {
	Enabled bool `json:"enabled"` // mặc định: false
}

// ParseTaskContractConfig đọc khóa task_contract trong settings của team.
// Bản Lite không có tính năng này nên luôn trả Enabled=false.
func ParseTaskContractConfig(settings json.RawMessage) TaskContractConfig {
	var cfg TaskContractConfig
	if !edition.Current().TeamFullMode || len(settings) == 0 {
		return cfg
	}
	var s struct {
		TaskContract *struct {
			Enabled *bool `json:"enabled"`
		} `json:"task_contract"`
	}
	if json.Unmarshal(settings, &s) != nil || s.TaskContract == nil || s.TaskContract.Enabled == nil {
		return cfg
	}
	cfg.Enabled = *s.TaskContract.Enabled
	return cfg
}

// Các loại task hợp lệ. escalation còn do hệ thống tự tạo (task chờ người
// duyệt, xem createEscalationTask); delegation và escalation cũng được tạo từ
// dialog của web UI.
const (
	TaskTypeGeneral    = "general"
	TaskTypeRequest    = "request"
	TaskTypeNote       = "note"
	TaskTypeDelegation = "delegation"
	TaskTypeEscalation = "escalation"
)

// ErrTaskTypeInvalid: task_type gửi vào create không thuộc tập hợp lệ.
var ErrTaskTypeInvalid = errors.New("invalid task_type: must be general, request, note, delegation or escalation")

// NormalizeCreateTaskType chuẩn hoá task_type của lệnh create (tool, RPC,
// MCP): rỗng thành general; giá trị ngoài tập hợp lệ trả ErrTaskTypeInvalid
// để lỗi gõ (vd "generl") không âm thầm tạo task loại lạ.
func NormalizeCreateTaskType(taskType string) (string, error) {
	switch taskType {
	case "":
		return TaskTypeGeneral, nil
	case TaskTypeGeneral, TaskTypeRequest, TaskTypeNote, TaskTypeDelegation, TaskTypeEscalation:
		return taskType, nil
	default:
		return "", fmt.Errorf("%w (got %q)", ErrTaskTypeInvalid, taskType)
	}
}

// TaskContractRequired cho biết task loại taskType của team có bắt buộc
// contract hay không khi team đã bật task_contract. Chỉ request và note được
// miễn. Mọi loại khác (general, delegation, escalation, rỗng và giá trị lạ từ
// dữ liệu cũ) đều bắt buộc, để cổng không bị né bằng cách đổi task_type rồi
// giao cho member. Task escalation do createEscalationTask tạo không có
// assignee và chỉ đi qua approve/reject, không qua cổng giao, nên luồng duyệt
// không bị ảnh hưởng.
func TaskContractRequired(team *store.TeamData, taskType string) bool {
	if team == nil || taskType == TaskTypeRequest || taskType == TaskTypeNote {
		return false
	}
	return ParseTaskContractConfig(team.Settings).Enabled
}

// ErrTaskContractMissing: team bắt buộc contract mà task chưa có.
var ErrTaskContractMissing = errors.New("this team requires a task contract (objective, deliverables, acceptance) before the task can be assigned or dispatched")

// CheckTaskContractForAssign kiểm tra task sắp được giao hoặc dispatch lại có
// chủ ý (assign, retry) ở team đã bật task_contract: task thuộc loại bắt
// buộc (xem TaskContractRequired) phải có contract hợp lệ. Trả ErrTaskContractMissing khi chưa có, lỗi Validate khi
// contract sai, nil khi được phép hoặc team không bắt buộc.
func CheckTaskContractForAssign(team *store.TeamData, task *store.TeamTaskData) error {
	if task == nil || !TaskContractRequired(team, task.TaskType) {
		return nil
	}
	c := TaskContractFromTask(task)
	if c == nil {
		return ErrTaskContractMissing
	}
	return c.Validate()
}
