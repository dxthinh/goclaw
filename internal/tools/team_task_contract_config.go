package tools

import (
	"encoding/json"

	"github.com/nextlevelbuilder/goclaw/internal/edition"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// TaskContractConfig bật Task Contract cho một team. Khi bật, task loại
// general phải kèm contract hợp lệ mới được tạo hoặc giao cho member.
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

// TaskContractRequired cho biết task loại taskType của team có bắt buộc
// contract hay không: chỉ task general ở team đã bật. Task request và note
// không bắt buộc. taskType rỗng được hiểu là general.
func TaskContractRequired(team *store.TeamData, taskType string) bool {
	if team == nil {
		return false
	}
	if taskType == "" {
		taskType = "general"
	}
	return taskType == "general" && ParseTaskContractConfig(team.Settings).Enabled
}
