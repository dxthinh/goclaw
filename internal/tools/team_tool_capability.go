package tools

import (
	"context"
	"slices"

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
	// tenantCfgs cho danh sách tool bị tắt theo tenant, cùng nguồn mà resolver
	// nạp vào Loop.disabledTools (bị bỏ khi chạy). nil: không loại gì.
	tenantCfgs store.BuiltinToolTenantConfigStore
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

// WithTenantToolConfigs gắn nguồn tool bị tắt theo tenant để kiểm tra needs
// khớp với lúc chạy. Trả chính checker (nil vẫn là nil).
func (c *TeamCapabilityChecker) WithTenantToolConfigs(s store.BuiltinToolTenantConfigStore) *TeamCapabilityChecker {
	if c != nil {
		c.tenantCfgs = s
	}
	return c
}

// TenantDisabledTools đọc tool bị tắt cho tenant, giống resolver: lỗi hoặc
// tenant rỗng thì coi như không tắt gì (lúc chạy cũng vậy).
func (c *TeamCapabilityChecker) TenantDisabledTools(ctx context.Context, tenantID uuid.UUID) map[string]bool {
	if c == nil || c.tenantCfgs == nil || tenantID == uuid.Nil {
		return nil
	}
	names, err := c.tenantCfgs.ListDisabled(ctx, tenantID)
	if err != nil || len(names) == 0 {
		return nil
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
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
// một task của team trên loại kênh channelType. Chạy đúng chuỗi lọc của lượt
// chạy: FilterTools với policy agent dựng như resolver, rồi RunToolFilter như
// Loop.buildFilteredTools. Các bước phụ thuộc dữ liệu mà lúc giao chưa có
// được xử lý như sau:
//   - groupToolAllow theo request (RunRequest.ToolAllow): không có, truyền nil.
//   - chế độ điều phối: member của team luôn ở ModeTeam nên không ẩn gì.
//   - bootstrap: chỉ áp cho lượt đầu của agent open còn BOOTSTRAP.md, không
//     biết trước được nên bỏ qua (không ẩn).
//   - kênh: channelType rỗng là không biết kênh (task từ dashboard RPC, MCP,
//     lượt được ủy quyền) nên bỏ qua bước lọc kênh; quyền theo kênh của
//     telegram_manager cũng chưa biết nên chỉ xét loại kênh.
//
// Bỏ qua theo hướng không chặn: lúc chạy member vẫn báo lại được qua clarify.
// Tool MCP cấp riêng theo agent hoặc user không nằm trong registry chung nên
// không có ở đây.
func (c *TeamCapabilityChecker) EffectiveTools(agent *store.AgentData, disabled map[string]bool, channelType string) map[string]bool {
	if c == nil || agent == nil {
		return nil
	}
	policy := teamMemberToolPolicy(agent.ParseToolsConfig())
	defs := c.pe.FilterTools(c.reg, agent.AgentKey, agent.Provider, policy, nil, false, false)
	filter := RunToolFilter{
		Disabled:        disabled,
		HideSkillManage: !AgentSkillEvolveEnabled(agent),
	}
	if channelType != "" {
		filter.Channel = &ChannelToolFilter{Registry: c.reg, Type: channelType, IgnoreTelegramManagerPermissions: true}
	}
	defs = filter.Apply(defs, nil)
	set := make(map[string]bool, len(defs))
	for _, d := range defs {
		if d.Function != nil {
			set[d.Function.Name] = true
		}
	}
	return set
}

// MissingNeeds trả các tên trong needs mà agent chưa có đủ tool, sau khi bỏ
// các tool tenant đã tắt (disabled) và tool bị ẩn trên kênh channelType
// (rỗng: không lọc theo kênh).
func (c *TeamCapabilityChecker) MissingNeeds(agent *store.AgentData, needs []string, disabled map[string]bool, channelType string) []string {
	if c == nil || len(needs) == 0 {
		return nil
	}
	return c.missingFrom(c.EffectiveTools(agent, disabled, channelType), needs, c.registeredSet(), channelType)
}

// MembersSatisfying trả agent_key của các agent có đủ mọi tên trong needs, bỏ
// qua các ID trong exclude (lead, assignee hiện tại), theo thứ tự của agents.
func (c *TeamCapabilityChecker) MembersSatisfying(agents []store.AgentData, needs []string, disabled map[string]bool, channelType string, exclude ...uuid.UUID) []string {
	if c == nil {
		return nil
	}
	registered := c.registeredSet()
	var keys []string
	for i := range agents {
		if slices.Contains(exclude, agents[i].ID) {
			continue
		}
		if len(c.missingFrom(c.EffectiveTools(&agents[i], disabled, channelType), needs, registered, channelType)) == 0 {
			keys = append(keys, agents[i].AgentKey)
		}
	}
	return keys
}
