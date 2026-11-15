package methods

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

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
// thuộc loại bắt buộc (mọi loại trừ request, note) sắp được giao hoặc retry chưa có contract hợp lệ. Rỗng khi được phép.
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

// SetToolCapabilities nối checker năng lực tool để kiểm tra contract.needs khi
// giao task từ dashboard. Chưa nối thì bỏ qua kiểm tra.
func (m *TeamsMethods) SetToolCapabilities(c *tools.TeamCapabilityChecker) { m.toolCaps = c }

// taskNeedsError trả lỗi đã dịch khi assignee thiếu nhóm tool hoặc tool trong
// contract.needs (team đã bật task_contract). Rỗng khi được phép, khi contract
// không có needs hoặc team chưa bật; khi đó không truy vấn gì thêm.
func (m *TeamsMethods) taskNeedsError(ctx context.Context, locale string, team *store.TeamData, contract *tools.TaskContract, assigneeID uuid.UUID) string {
	err := m.toolCaps.CheckTaskNeedsInTeam(ctx, m.teamStore, m.agentStore, team, contract, assigneeID)
	var needsErr *tools.TaskNeedsError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &needsErr) && len(needsErr.Satisfying) == 0:
		return i18n.T(locale, i18n.MsgTaskNeedsNoMember, needsErr.Assignee, strings.Join(needsErr.Missing, ", "))
	case errors.As(err, &needsErr):
		return i18n.T(locale, i18n.MsgTaskNeedsMissing, needsErr.Assignee, strings.Join(needsErr.Missing, ", "), strings.Join(needsErr.Satisfying, ", "))
	default:
		return i18n.T(locale, i18n.MsgTaskContractInvalid, err.Error())
	}
}
