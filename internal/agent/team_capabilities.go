package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// teamMemberCapabilities tính nhóm tool khác biệt giữa các member (bỏ lead)
// cho TEAM.md của lead. Chỉ làm khi selfID là lead của team đã bật
// task_contract. Trả thêm agent_key của các member để router xóa cache của
// lead khi tool của member đổi. caps nil khi mọi member giống nhau.
func teamMemberCapabilities(ctx context.Context, pe *tools.PolicyEngine, reg tools.ToolExecutor, agents store.AgentStore,
	team *store.TeamData, members []store.TeamMemberData, selfID uuid.UUID) (caps map[uuid.UUID][]string, dependsOn []string) {
	if team == nil || team.LeadAgentID != selfID || agents == nil || !tools.ParseTaskContractConfig(team.Settings).Enabled {
		return nil, nil
	}
	checker := tools.NewTeamCapabilityChecker(pe, reg)
	if checker == nil {
		return nil, nil
	}
	var ids []uuid.UUID
	for _, m := range members {
		if m.AgentID != team.LeadAgentID {
			ids = append(ids, m.AgentID)
			dependsOn = append(dependsOn, m.AgentKey)
		}
	}
	if len(ids) < 2 {
		return nil, dependsOn
	}
	data, err := agents.GetByIDs(ctx, ids)
	if err != nil {
		return nil, dependsOn
	}
	return checker.MemberToolGroupDiff(data), dependsOn
}

// buildTeamCapabilitiesMD viết mỗi member một dòng: nhóm tool có và thiếu,
// chỉ tính các nhóm mà không phải member nào cũng có. Rỗng khi caps rỗng.
func buildTeamCapabilitiesMD(members []store.TeamMemberData, caps map[uuid.UUID][]string) string {
	if len(caps) == 0 {
		return ""
	}
	var differing []string
	for _, groups := range caps {
		for _, g := range groups {
			if !slices.Contains(differing, g) {
				differing = append(differing, g)
			}
		}
	}
	slices.Sort(differing)
	var sb strings.Builder
	sb.WriteString("\n## Member Tool Groups\n")
	sb.WriteString("Tool groups that only some members have. List the groups a task requires in the contract's `needs`; assigning it to a member without them is rejected.\n\n")
	for _, m := range members {
		has, ok := caps[m.AgentID]
		if !ok {
			continue
		}
		var lacks []string
		for _, g := range differing {
			if !slices.Contains(has, g) {
				lacks = append(lacks, g)
			}
		}
		hasText := "none"
		if len(has) > 0 {
			hasText = strings.Join(has, ", ")
		}
		line := fmt.Sprintf("- **%s**: has %s", m.AgentKey, hasText)
		if len(lacks) > 0 {
			line += "; lacks " + strings.Join(lacks, ", ")
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

// dependsOnAgent cho biết Agent trong cache có prompt phụ thuộc agentKey.
func dependsOnAgent(entry *agentEntry, agentKey string) bool {
	if entry == nil || agentKey == "" {
		return false
	}
	d, ok := entry.agent.(cacheDependent)
	return ok && slices.Contains(d.DependsOnAgentKeys(), agentKey)
}
