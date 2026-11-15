package tools

import (
	"context"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// SetToolPolicy nối PolicyEngine và registry chung để kiểm tra needs của
// contract khi giao task. Chưa gọi thì mọi kiểm tra needs được bỏ qua.
func (m *TeamToolManager) SetToolPolicy(pe *PolicyEngine, reg ToolExecutor) {
	m.capabilities = NewTeamCapabilityChecker(pe, reg)
}

// ToolCapabilities trả checker đã nối, nil khi chưa nối.
func (m *TeamToolManager) ToolCapabilities() *TeamCapabilityChecker { return m.capabilities }

// teamTaskNeedsChecker là phần mở rộng tùy chọn của TeamToolBackend; backend
// không cài (mock trong test) thì tool bỏ qua kiểm tra needs.
type teamTaskNeedsChecker interface {
	CheckTaskNeeds(ctx context.Context, team *store.TeamData, members []store.TeamMemberData, contract *TaskContract, assigneeID uuid.UUID) error
	AssigneeToolWarning(ctx context.Context, team *store.TeamData, assigneeID uuid.UUID) string
}

var _ teamTaskNeedsChecker = (*TeamToolManager)(nil)

// CheckTaskNeeds áp cổng needs (xem TeamCapabilityChecker.CheckTaskNeeds) với
// agent store của manager.
func (m *TeamToolManager) CheckTaskNeeds(ctx context.Context, team *store.TeamData, members []store.TeamMemberData, contract *TaskContract, assigneeID uuid.UUID) error {
	return m.capabilities.CheckTaskNeeds(ctx, m.agentStore, team, members, contract, assigneeID)
}

// AssigneeToolWarning trả cảnh báo (tiếng Anh, cho LLM) khi policy của
// assignee ẩn team_tasks: member đó không báo tiến độ hay bình luận được trên
// task. Chỉ kiểm ở team đã bật task_contract; rỗng khi không có gì cần báo.
func (m *TeamToolManager) AssigneeToolWarning(ctx context.Context, team *store.TeamData, assigneeID uuid.UUID) string {
	if m.capabilities == nil || team == nil || !ParseTaskContractConfig(team.Settings).Enabled {
		return ""
	}
	ag, err := m.cachedGetAgentByID(ctx, assigneeID)
	if err != nil || ag == nil {
		return ""
	}
	if m.capabilities.EffectiveTools(ag)["team_tasks"] {
		return ""
	}
	return "Warning: the assignee's tool policy hides team_tasks, so it cannot report progress or comment on this task. Ask an admin to allow team_tasks for " + ag.AgentKey + "."
}

// checkTaskNeedsFromBackend gọi cổng needs khi backend hỗ trợ.
func checkTaskNeedsFromBackend(ctx context.Context, backend TeamToolBackend, team *store.TeamData, members []store.TeamMemberData, contract *TaskContract, assigneeID uuid.UUID) error {
	c, ok := backend.(teamTaskNeedsChecker)
	if !ok {
		return nil
	}
	return c.CheckTaskNeeds(ctx, team, members, contract, assigneeID)
}
