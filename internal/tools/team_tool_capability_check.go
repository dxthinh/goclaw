package tools

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// TaskNeedsError: assignee thiếu nhóm tool hoặc tool mà contract khai báo
// trong needs. Satisfying rỗng nghĩa là không member nào đủ.
type TaskNeedsError struct {
	Assignee   string
	Missing    []string
	Satisfying []string
}

func (e *TaskNeedsError) Error() string {
	base := fmt.Sprintf("assignee %q lacks the tools this task needs: %s.", e.Assignee, strings.Join(e.Missing, ", "))
	if len(e.Satisfying) == 0 {
		return base + " No team member has them; ask an admin to grant these tools to a member, or remove them from contract.needs."
	}
	return base + " Members that have them: " + strings.Join(e.Satisfying, ", ") + ". Assign the task to one of them instead."
}

// CheckTaskNeeds là cổng dùng chung cho tool, RPC và MCP khi giao task (tạo
// kèm giao, assign, retry). Chỉ kiểm khi team đã bật task_contract và contract
// có needs; ngoài ra trả nil, không truy vấn gì. Trả lỗi của ValidateNeeds khi
// needs có tên lạ, *TaskNeedsError khi assignee thiếu tool.
func (c *TeamCapabilityChecker) CheckTaskNeeds(ctx context.Context, agents store.AgentStore, team *store.TeamData, members []store.TeamMemberData, contract *TaskContract, assigneeID uuid.UUID) error {
	if contract == nil || len(contract.Needs) == 0 || team == nil || !ParseTaskContractConfig(team.Settings).Enabled {
		return nil
	}
	if c == nil || agents == nil {
		slog.Warn("team.task_needs_check_skipped", "team_id", team.ID, "reason", "tool policy not wired")
		return nil
	}
	if err := c.ValidateNeeds(contract.Needs); err != nil {
		return err
	}
	ids := []uuid.UUID{assigneeID}
	for _, m := range members {
		if m.AgentID != assigneeID && m.AgentID != team.LeadAgentID {
			ids = append(ids, m.AgentID)
		}
	}
	data, err := agents.GetByIDs(ctx, ids)
	if err != nil {
		return fmt.Errorf("failed to load member tool settings: %w", err)
	}
	idx := slices.IndexFunc(data, func(a store.AgentData) bool { return a.ID == assigneeID })
	if idx < 0 {
		return nil // assignee không tồn tại: các kiểm tra khác đã/ sẽ báo lỗi
	}
	// Tool bị tắt ở tenant bị bỏ lúc chạy, nên cũng bị bỏ ở đây. Mọi member
	// cùng tenant với assignee; chỉ đọc khi đã chắc phải kiểm needs.
	disabled := c.TenantDisabledTools(ctx, data[idx].TenantID)
	missing := c.MissingNeeds(&data[idx], contract.Needs, disabled)
	if len(missing) == 0 {
		return nil
	}
	return &TaskNeedsError{
		Assignee:   data[idx].AgentKey,
		Missing:    missing,
		Satisfying: c.MembersSatisfying(data, contract.Needs, disabled, assigneeID, team.LeadAgentID),
	}
}

// CheckTaskNeedsInTeam giống CheckTaskNeeds nhưng tự đọc danh sách member từ
// teams, và chỉ đọc khi thật sự cần kiểm (team bật contract, contract có
// needs). Dùng cho RPC và MCP, nơi chưa có sẵn danh sách member.
func (c *TeamCapabilityChecker) CheckTaskNeedsInTeam(ctx context.Context, teams store.TeamStore, agents store.AgentStore, team *store.TeamData, contract *TaskContract, assigneeID uuid.UUID) error {
	if contract == nil || len(contract.Needs) == 0 || team == nil || !ParseTaskContractConfig(team.Settings).Enabled {
		return nil
	}
	members, err := teams.ListMembers(ctx, team.ID)
	if err != nil {
		return fmt.Errorf("failed to load team members: %w", err)
	}
	return c.CheckTaskNeeds(ctx, agents, team, members, contract, assigneeID)
}

// MemberToolGroupDiff trả, cho từng agent, các nhóm tool nó có trong số những
// nhóm mà chỉ một phần agent có (nhóm ai cũng có hoặc không ai có bị bỏ).
// disabled là tool tenant đã tắt. Trả nil khi mọi agent giống nhau. Mọi
// agent đều có khóa trong map kết quả.
func (c *TeamCapabilityChecker) MemberToolGroupDiff(agents []store.AgentData, disabled map[string]bool) map[uuid.UUID][]string {
	if c == nil || len(agents) < 2 {
		return nil
	}
	registered := c.registeredSet()
	groups := TeamCapabilityGroups()
	has := make([]map[string]bool, len(agents))
	count := make(map[string]int, len(groups))
	for i := range agents {
		eff := c.EffectiveTools(&agents[i], disabled)
		has[i] = make(map[string]bool, len(groups))
		for _, g := range groups {
			if len(c.missingFrom(eff, []string{g}, registered)) == 0 {
				has[i][g] = true
				count[g]++
			}
		}
	}
	var differing []string
	for _, g := range groups {
		if count[g] > 0 && count[g] < len(agents) {
			differing = append(differing, g)
		}
	}
	if len(differing) == 0 {
		return nil
	}
	out := make(map[uuid.UUID][]string, len(agents))
	for i := range agents {
		list := []string{}
		for _, g := range differing {
			if has[i][g] {
				list = append(list, g)
			}
		}
		out[agents[i].ID] = list
	}
	return out
}
