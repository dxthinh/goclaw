package tools

import (
	"slices"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// RunToolFilter là chuỗi lọc tool áp sau PolicyEngine.FilterTools ở mỗi lượt
// chạy của agent (Loop.buildFilteredTools). Đặt ở package tools để cả vòng lặp
// agent lẫn kiểm tra contract.needs khi giao task (TeamCapabilityChecker) gọi
// cùng một hàm, tránh lệch giữa tập tool lúc giao và lúc chạy. Các bước chạy
// theo thứ tự khai báo dưới đây; bước có dữ liệu rỗng thì bỏ qua.
type RunToolFilter struct {
	// DenyNames ẩn tool theo chế độ điều phối (spawn/delegate). Xóa khỏi allowed.
	DenyNames map[string]bool
	// Disabled là tool tenant đã tắt. Xóa khỏi allowed.
	Disabled map[string]bool
	// BootstrapAllow khác nil nghĩa là lượt đang ở chế độ bootstrap (agent
	// open): chỉ giữ các tên trong map. Không đụng allowed.
	BootstrapAllow map[string]bool
	// HideSkillManage ẩn skill_manage khi agent tắt skill_evolve. Tool vẫn ở
	// registry. Không đụng allowed.
	HideSkillManage bool
	// Channel lọc tool ChannelAware theo loại kênh; nil thì bỏ qua bước này.
	// Xóa khỏi allowed.
	Channel *ChannelToolFilter
}

// ChannelToolFilter là dữ liệu cho bước lọc theo kênh.
type ChannelToolFilter struct {
	// Registry tra tool theo tên để biết tool có cài ChannelAware không; nil
	// thì mọi tool trừ telegram_manager đều hiện.
	Registry interface {
		Get(name string) (Tool, bool)
	}
	Type                       string
	TelegramManagerPermissions []string
	// IgnoreTelegramManagerPermissions bỏ điều kiện quyền theo kênh của
	// telegram_manager, chỉ xét loại kênh. Dùng khi giao task, lúc chưa biết
	// quyền mà kênh sẽ cấp cho lượt chạy.
	IgnoreTelegramManagerPermissions bool
}

// AgentSkillEvolveEnabled cho biết agent có được dùng skill_manage không:
// chỉ agent predefined bật skill_evolve.
func AgentSkillEvolveEnabled(ag *store.AgentData) bool {
	return ag != nil && ag.AgentType == store.AgentTypePredefined && ag.ParseSkillEvolve()
}

// Apply chạy chuỗi lọc trên defs (kết quả của FilterTools) và cập nhật allowed
// (có thể nil) đúng như vòng lặp agent vẫn làm.
func (f RunToolFilter) Apply(defs []providers.ToolDefinition, allowed map[string]bool) []providers.ToolDefinition {
	if len(f.DenyNames) > 0 {
		defs = dropTools(defs[:0:0], defs, allowed, func(name string) bool { return f.DenyNames[name] })
	}
	if len(f.Disabled) > 0 {
		defs = dropTools(defs[:0], defs, allowed, func(name string) bool { return f.Disabled[name] })
	}
	if f.BootstrapAllow != nil {
		var kept []providers.ToolDefinition
		for _, td := range defs {
			if td.Function != nil && f.BootstrapAllow[td.Function.Name] {
				kept = append(kept, td)
			}
		}
		defs = kept
	}
	if f.HideSkillManage {
		defs = dropTools(defs[:0:0], defs, nil, func(name string) bool { return name == "skill_manage" })
	}
	if ch := f.Channel; ch != nil {
		defs = dropTools(defs[:0:0], defs, allowed, func(name string) bool {
			return !ToolVisibleForChannel(ch.Registry, name, ch.Type, ch.TelegramManagerPermissions, ch.IgnoreTelegramManagerPermissions)
		})
	}
	return defs
}

// dropTools ghi vào dst các def không bị drop, xóa tên bị drop khỏi allowed.
func dropTools(dst, defs []providers.ToolDefinition, allowed map[string]bool, drop func(string) bool) []providers.ToolDefinition {
	for _, td := range defs {
		if td.Function != nil && drop(td.Function.Name) {
			if allowed != nil {
				delete(allowed, td.Function.Name)
			}
			continue
		}
		dst = append(dst, td)
	}
	return dst
}

// ToolVisibleForChannel quyết định tool có hiện trên loại kênh hay không:
// telegram_manager chỉ hiện trên telegram và (trừ khi ignorePerms) khi kênh
// cấp quyền; tool cài ChannelAware chỉ hiện trên các loại kênh nó khai báo và
// bị ẩn khi không có loại kênh; tool khác luôn hiện.
func ToolVisibleForChannel(reg interface {
	Get(name string) (Tool, bool)
}, name, channelType string, telegramManagerPermissions []string, ignorePerms bool) bool {
	if name == "telegram_manager" {
		return channelType == "telegram" && (ignorePerms || len(telegramManagerPermissions) > 0)
	}
	if reg == nil {
		return true
	}
	tool, ok := reg.Get(name)
	if !ok {
		return true
	}
	ca, ok := tool.(ChannelAware)
	if !ok {
		return true
	}
	if channelType == "" {
		return false
	}
	return slices.Contains(ca.RequiredChannelTypes(), channelType)
}
