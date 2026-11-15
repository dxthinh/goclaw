package tools

import (
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// TeamWorkspaceTools là các tool file mà resolver luôn thêm vào alsoAllow của
// agent thuộc một team (agentToolPolicyWithWorkspace trong internal/agent).
// Test parity ở internal/agent giữ hai nơi không lệch nhau.
var TeamWorkspaceTools = []string{"read_file", "write_file", "list_files"}

// teamCapabilityGroupSkip là nhóm không dùng để so năng lực giữa member: nhóm
// gộp "goclaw" gần như ai cũng thiếu một phần nên không mang thông tin.
var teamCapabilityGroupSkip = map[string]bool{"goclaw": true}

// TeamCapabilityChecker tính tool thực tế của một agent trong team bằng chính
// PolicyEngine và registry mà vòng lặp agent dùng, để kiểm tra trường needs
// của contract trước khi giao task. Con trỏ nil là hợp lệ: mọi kiểm tra được
// bỏ qua (kèm log cảnh báo khi có needs cần kiểm).
type TeamCapabilityChecker struct {
	pe  *PolicyEngine
	reg *Registry
}

// NewTeamCapabilityChecker trả nil khi thiếu PolicyEngine hoặc registry cụ thể.
func NewTeamCapabilityChecker(pe *PolicyEngine, executor ToolExecutor) *TeamCapabilityChecker {
	if pe == nil || executor == nil {
		return nil
	}
	reg := ResolveConcreteRegistry(executor)
	if reg == nil {
		return nil
	}
	return &TeamCapabilityChecker{pe: pe, reg: reg}
}

// teamMemberToolPolicy dựng policy giống resolver dựng cho agent trong team:
// policy của agent cộng các tool file của workspace team. Không sửa policy gốc.
func teamMemberToolPolicy(policy *config.ToolPolicySpec) *config.ToolPolicySpec {
	var out config.ToolPolicySpec
	if policy != nil {
		out = *policy
	}
	out.AlsoAllow = slices.Clone(out.AlsoAllow)
	for _, name := range TeamWorkspaceTools {
		if !slices.Contains(out.AlsoAllow, name) {
			out.AlsoAllow = append(out.AlsoAllow, name)
		}
	}
	return &out
}

// EffectiveTools trả tập tên tool (kể cả alias) mà agent được dùng khi chạy
// trong team: FilterTools với policy của agent, không có groupToolAllow theo
// request, không phải subagent. Tool MCP cấp riêng theo agent hoặc user không
// nằm trong registry chung nên không có ở đây.
func (c *TeamCapabilityChecker) EffectiveTools(agent *store.AgentData) map[string]bool {
	if c == nil || agent == nil {
		return nil
	}
	policy := teamMemberToolPolicy(agent.ParseToolsConfig())
	defs := c.pe.FilterTools(c.reg, agent.AgentKey, agent.Provider, policy, nil, false, false)
	set := make(map[string]bool, len(defs))
	for _, d := range defs {
		if d.Function != nil {
			set[d.Function.Name] = true
		}
	}
	return set
}

// needTools đổi một tên trong needs thành danh sách tool phải có. Tên nhóm
// (có hoặc không có tiền tố "group:") thành các tool của nhóm đang có trong
// registry; tên khác được coi là tên tool. known=false khi tên không phải
// nhóm và không phải tool đã đăng ký.
func (c *TeamCapabilityChecker) needTools(need string, registered map[string]bool) (toolNames []string, known bool) {
	name := strings.TrimPrefix(need, "group:")
	if members, ok := c.reg.GetToolGroup(name); ok {
		for _, m := range members {
			if registered[m] {
				toolNames = append(toolNames, m)
			}
		}
		return toolNames, true
	}
	if _, ok := c.reg.Get(need); ok {
		return []string{need}, true
	}
	return nil, false
}

func (c *TeamCapabilityChecker) registeredSet() map[string]bool {
	list := c.reg.List()
	set := make(map[string]bool, len(list))
	for _, n := range list {
		set[n] = true
	}
	return set
}

// ValidateNeeds báo lỗi khi needs có tên không phải nhóm tool và không phải
// tool đã đăng ký, kèm danh sách nhóm hợp lệ.
func (c *TeamCapabilityChecker) ValidateNeeds(needs []string) error {
	if c == nil || len(needs) == 0 {
		return nil
	}
	registered := c.registeredSet()
	var unknown []string
	for _, n := range needs {
		if _, ok := c.needTools(n, registered); !ok {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	return fmt.Errorf("needs has unknown names: %s. Use a tool group (%s) or a registered tool name",
		strings.Join(unknown, ", "), strings.Join(TeamCapabilityGroups(), ", "))
}

// TeamCapabilityGroups là các nhóm tool dùng được trong needs và trong tóm tắt
// năng lực của TEAM.md, theo thứ tự cố định.
func TeamCapabilityGroups() []string {
	groups := make([]string, 0, len(builtinToolGroups))
	for g := range builtinToolGroups {
		if !teamCapabilityGroupSkip[g] {
			groups = append(groups, g)
		}
	}
	slices.Sort(groups)
	return groups
}

// missingFrom trả các tên trong needs mà tập tool effective chưa đủ. Một nhóm
// đủ khi mọi tool của nhóm có trong registry đều được phép; nhóm không có tool
// nào trong registry thì không ai có.
func (c *TeamCapabilityChecker) missingFrom(effective map[string]bool, needs []string, registered map[string]bool) []string {
	var missing []string
	for _, n := range needs {
		toolNames, known := c.needTools(n, registered)
		ok := known && len(toolNames) > 0
		for _, t := range toolNames {
			if !effective[t] {
				ok = false
				break
			}
		}
		if !ok {
			missing = append(missing, n)
		}
	}
	return missing
}

// MissingNeeds trả các tên trong needs mà agent chưa có đủ tool.
func (c *TeamCapabilityChecker) MissingNeeds(agent *store.AgentData, needs []string) []string {
	if c == nil || len(needs) == 0 {
		return nil
	}
	return c.missingFrom(c.EffectiveTools(agent), needs, c.registeredSet())
}

// MembersSatisfying trả agent_key của các agent có đủ mọi tên trong needs, bỏ
// qua các ID trong exclude (lead, assignee hiện tại), theo thứ tự của agents.
func (c *TeamCapabilityChecker) MembersSatisfying(agents []store.AgentData, needs []string, exclude ...uuid.UUID) []string {
	if c == nil {
		return nil
	}
	registered := c.registeredSet()
	var keys []string
	for i := range agents {
		if slices.Contains(exclude, agents[i].ID) {
			continue
		}
		if len(c.missingFrom(c.EffectiveTools(&agents[i]), needs, registered)) == 0 {
			keys = append(keys, agents[i].AgentKey)
		}
	}
	return keys
}
