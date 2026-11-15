package methods

import (
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// resolveTaskCreateContract kiểm tra contract gửi kèm teams.tasks.create.
// Contract có mặt thì luôn phải hợp lệ. Thiếu contract chỉ bị từ chối khi team
// bật task_contract và task được giao ngay (assignTo); task backlog chưa giao
// được tạo không cần contract và bị chặn ở bước giao.
// Trả thông báo lỗi đã dịch, rỗng khi hợp lệ.
func resolveTaskCreateContract(locale string, team *store.TeamData, raw map[string]any, taskType string, assigning bool) (*tools.TaskContract, string) {
	if raw == nil {
		if assigning && tools.TaskContractRequired(team, taskType) {
			return nil, i18n.T(locale, i18n.MsgTaskContractRequired)
		}
		return nil, ""
	}
	contract, err := tools.ParseTaskContract(raw)
	if err != nil {
		return nil, i18n.T(locale, i18n.MsgTaskContractInvalid, err.Error())
	}
	if contract == nil {
		return resolveTaskCreateContract(locale, team, nil, taskType, assigning)
	}
	// Amendment và version do hệ thống ghi.
	contract.Amendments = nil
	contract.Version = 1
	if err := contract.Validate(); err != nil {
		return nil, i18n.T(locale, i18n.MsgTaskContractInvalid, err.Error())
	}
	return contract, ""
}

// taskAssignContractError trả lỗi đã dịch khi team bật task_contract mà task
// general sắp được giao chưa có contract hợp lệ. Rỗng khi được phép giao.
func taskAssignContractError(locale string, team *store.TeamData, task *store.TeamTaskData) string {
	if !tools.TaskContractRequired(team, task.TaskType) {
		return ""
	}
	contract := tools.TaskContractFromTask(task)
	if contract == nil {
		return i18n.T(locale, i18n.MsgTaskContractRequired)
	}
	if err := contract.Validate(); err != nil {
		return i18n.T(locale, i18n.MsgTaskContractInvalid, err.Error())
	}
	return ""
}
