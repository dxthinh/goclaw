package tools

import (
	"context"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// SetToolPolicy nối PolicyEngine, registry chung và nguồn tool bị tắt theo
// tenant để kiểm tra needs của contract khi giao task. Chưa gọi thì mọi kiểm
// tra needs được bỏ qua.
func (m *TeamToolManager) SetToolPolicy(pe *PolicyEngine, reg ToolExecutor, tenantCfgs store.BuiltinToolTenantConfigStore) {
	m.capabilities = NewTeamCapabilityChecker(pe, reg).WithTenantToolConfigs(tenantCfgs)
}

// ToolCapabilities trả checker đã nối, nil khi chưa nối.
func (m *TeamToolManager) ToolCapabilities() *TeamCapabilityChecker { return m.capabilities }

// teamTaskNeedsChecker là phần mở rộng tùy chọn của TeamToolBackend; backend
// không cài (mock trong test) thì tool bỏ qua kiểm tra needs.
type teamTaskNeedsChecker interface {
	// CheckAssignee áp cổng needs; warnHidden=true thì còn trả cảnh báo khi
	// assignee không dùng được team_tasks. Hai việc dùng chung một lần đọc
	// assignee và tool bị tắt theo tenant.
	// channelType là loại kênh task sẽ chạy, rỗng khi không biết (không lọc theo kênh).
	CheckAssignee(ctx context.Context, team *store.TeamData, members []store.TeamMemberData, contract *TaskContract, assigneeID uuid.UUID, channelType string, warnHidden bool) (string, error)
}

var _ teamTaskNeedsChecker = (*TeamToolManager)(nil)

// CheckAssignee áp cổng needs (xem TeamCapabilityChecker.CheckTaskNeeds) với
// agent store của manager, và khi warnHidden thì trả thêm cảnh báo team_tasks
// bị ẩn. Chỉ làm ở team đã bật task_contract. Danh sách tool bị tắt theo
// tenant được đọc tối đa một lần cho cả hai việc.
func (m *TeamToolManager) CheckAssignee(ctx context.Context, team *store.TeamData, members []store.TeamMemberData, contract *TaskContract, assigneeID uuid.UUID, channelType string, warnHidden bool) (string, error) {
	assignee, disabled, err := m.capabilities.checkTaskNeeds(ctx, m.agentStore, team, members, contract, assigneeID, channelType)
	if err != nil || !warnHidden || m.capabilities == nil || team == nil || !ParseTaskContractConfig(team.Settings).Enabled {
		return "", err
	}
	if assignee == nil {
		// Không có needs nên cổng chưa đọc gì: lấy assignee từ cache, đọc tool bị tắt một lần.
		ag, err := m.cachedGetAgentByID(ctx, assigneeID)
		if err != nil || ag == nil {
			return "", nil
		}
		assignee, disabled = ag, m.capabilities.TenantDisabledTools(ctx, ag.TenantID)
	}
	return m.capabilities.HiddenTeamTasksWarning(assignee, disabled), nil
}

// checkTaskNeedsFromBackend gọi cổng needs khi backend hỗ trợ.
func checkTaskNeedsFromBackend(ctx context.Context, backend TeamToolBackend, team *store.TeamData, members []store.TeamMemberData, contract *TaskContract, assigneeID uuid.UUID, channelType string) error {
	c, ok := backend.(teamTaskNeedsChecker)
	if !ok {
		return nil
	}
	_, err := c.CheckAssignee(ctx, team, members, contract, assigneeID, channelType, false)
	return err
}

// taskChannelTypeFromCtx trả loại kênh mà member sẽ chạy task có kênh gốc
// taskChannel. Lúc chạy, loại kênh lấy từ kênh gốc của task; lượt của lead
// chỉ biết loại của kênh hiện tại, nên chỉ dùng được khi task giữ đúng kênh
// đó và lượt không được ủy quyền (kênh workspace khác kênh giao). Ngoài ra trả
// rỗng: không lọc tool theo kênh.
func taskChannelTypeFromCtx(ctx context.Context, taskChannel string) string {
	if taskChannel == "" || WorkspaceChannelFromCtx(ctx) != "" || taskChannel != ToolChannelFromCtx(ctx) {
		return ""
	}
	return ToolChannelTypeFromCtx(ctx)
}
