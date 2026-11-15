package methods

import (
	"errors"

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
	var input any
	if raw != nil {
		input = raw
	}
	contract, err := tools.NewTaskContractFromInput(input)
	if err != nil {
		return nil, i18n.T(locale, i18n.MsgTaskContractInvalid, err.Error())
	}
	if contract == nil && assigning && tools.TaskContractRequired(team, taskType) {
		return nil, i18n.T(locale, i18n.MsgTaskContractRequired)
	}
	return contract, ""
}

// taskAssignContractError trả lỗi đã dịch khi team bật task_contract mà task
// general sắp được giao hoặc retry chưa có contract hợp lệ. Rỗng khi được phép.
func taskAssignContractError(locale string, team *store.TeamData, task *store.TeamTaskData) string {
	err := tools.CheckTaskContractForAssign(team, task)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, tools.ErrTaskContractMissing):
		return i18n.T(locale, i18n.MsgTaskContractRequired)
	default:
		return i18n.T(locale, i18n.MsgTaskContractInvalid, err.Error())
	}
}
