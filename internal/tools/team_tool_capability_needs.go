package tools

import (
	"fmt"
	"slices"
	"strings"
)

// visibleOn áp phần lọc kênh của lúc chạy (ToolVisibleForChannel) cho loại
// kênh của task, chỉ xét loại kênh vì quyền telegram_manager chưa biết lúc
// giao. channelType rỗng là không biết kênh (dashboard RPC, MCP, lượt được ủy
// quyền): khi đó không lọc theo kênh.
func (c *TeamCapabilityChecker) visibleOn(name, channelType string) bool {
	if channelType == "" {
		return true
	}
	return ToolVisibleForChannel(c.reg, name, channelType, nil, true)
}

// needTools đổi một tên trong needs thành danh sách tool phải có. Tên nhóm
// (có hoặc không có tiền tố "group:") thành các tool của nhóm đang có trong
// registry và hiện được trên kênh của task (nhóm messaging gồm tool của nhiều
// kênh khác nhau, chỉ phần của kênh này là bắt buộc); tên khác được coi là
// tên tool và luôn bắt buộc. known=false khi tên không phải nhóm và không
// phải tool đã đăng ký.
func (c *TeamCapabilityChecker) needTools(need string, registered map[string]bool, channelType string) (toolNames []string, known bool) {
	name := strings.TrimPrefix(need, "group:")
	if members, ok := c.reg.GetToolGroup(name); ok {
		for _, m := range members {
			if registered[m] && c.visibleOn(m, channelType) {
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
		if _, ok := c.needTools(n, registered, ""); !ok {
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

// missingFrom trả các tên trong needs mà tập tool effective chưa đủ trên kênh
// channelType (rỗng: không lọc theo kênh). Một nhóm đủ khi mọi tool của nhóm
// có trong registry và hiện trên kênh đều được phép; nhóm không còn tool nào
// thì không ai có. Tool nêu đích danh mà bị ẩn trên kênh thì coi như thiếu.
func (c *TeamCapabilityChecker) missingFrom(effective map[string]bool, needs []string, registered map[string]bool, channelType string) []string {
	var missing []string
	for _, n := range needs {
		toolNames, known := c.needTools(n, registered, channelType)
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
