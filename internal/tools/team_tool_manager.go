package tools

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const teamCacheTTL = 5 * time.Minute

// teamCacheEntry wraps cached team data + members with a timestamp for TTL expiration.
type teamCacheEntry struct {
	team     *store.TeamData
	members  []store.TeamMemberData // loaded together with team to avoid separate DB call
	cachedAt time.Time
}

// agentCacheEntry wraps cached agent data with a timestamp for TTL expiration.
type agentCacheEntry struct {
	agent    *store.AgentData
	cachedAt time.Time
}

// TeamToolManager is the shared backend for team_tasks tool and workspace interceptor.
// It resolves the calling agent's team from context and provides access to
// the team store, agent store, and message bus.
// Includes a TTL cache for team data to avoid DB queries on every tool call.
type TeamToolManager struct {
	teamStore     store.TeamStore
	agentStore    store.AgentStore
	msgBus        *bus.MessageBus
	dataDir       string   // base data directory for workspace path resolution
	teamCache     sync.Map // agentID (uuid.UUID) → *teamCacheEntry
	agentCache    sync.Map // agentID (uuid.UUID) → *agentCacheEntry
	agentKeyCache sync.Map // agentKey (string) → *agentCacheEntry
	dispatchLocks sync.Map // teamID (uuid.UUID) → *sync.Mutex; tuần tự hóa dispatch theo team

	// approvalEnabled cho biết kênh theo tên có hỗ trợ và đã bật duyệt task qua
	// lệnh chat (channels.ChannelSupportsTaskApproval). Nối sau khi channel
	// manager được tạo; nil nghĩa là không kênh nào duyệt được.
	approvalEnabled atomic.Pointer[func(channelName string) bool]

	// actionPolicy là policy hành động của tool team_tasks (Full hoặc Lite),
	// để tin dispatch chỉ gợi ý hành động member được phép gọi. Nil = Full.
	actionPolicy TeamActionPolicy
}

func NewTeamToolManager(teamStore store.TeamStore, agentStore store.AgentStore, msgBus *bus.MessageBus, dataDir string) *TeamToolManager {
	return &TeamToolManager{teamStore: teamStore, agentStore: agentStore, msgBus: msgBus, dataDir: dataDir}
}

// ============================================================
// TeamToolBackend exported wrappers
// These thin wrappers satisfy the TeamToolBackend interface
// while keeping the unexported originals for internal use
// (WorkspaceInterceptor, PostTurnProcessor, etc.).
// ============================================================

func (m *TeamToolManager) Store() store.TeamStore { return m.teamStore }
func (m *TeamToolManager) DataDir() string        { return m.dataDir }
func (m *TeamToolManager) TryPublishInbound(msg bus.InboundMessage) bool {
	if m.msgBus == nil {
		return false
	}
	return m.msgBus.TryPublishInbound(msg)
}

// SetTaskApprovalChecker nối hàm kiểm tra kênh đã bật duyệt task qua lệnh chat.
func (m *TeamToolManager) SetTaskApprovalChecker(fn func(channelName string) bool) {
	if fn == nil {
		m.approvalEnabled.Store(nil)
		return
	}
	m.approvalEnabled.Store(&fn)
}

// ChannelTaskApprovalEnabled cho biết kênh channelName có duyệt task qua lệnh
// chat được không. Kênh nội bộ (teammate, system, dashboard) luôn là không.
func (m *TeamToolManager) ChannelTaskApprovalEnabled(channelName string) bool {
	switch channelName {
	case "", ChannelTeammate, ChannelSystem, ChannelDashboard:
		return false
	}
	fn := m.approvalEnabled.Load()
	return fn != nil && (*fn)(channelName)
}

// TaskFeedbackService là các thao tác feedback dùng chung cho RPC dashboard và
// lệnh duyệt trong chat (consumer). *TeamToolManager cài interface này.
type TaskFeedbackService interface {
	AnswerTask(ctx context.Context, team *store.TeamData, task *store.TeamTaskData, text string, assignee uuid.UUID, actor TaskActor) error
	RequestTaskChanges(ctx context.Context, team *store.TeamData, task *store.TeamTaskData, reason string, actor TaskActor) error
	ApproveUserCheckpoint(ctx context.Context, team *store.TeamData, task *store.TeamTaskData, actor TaskActor) error
	ChannelTaskApprovalEnabled(channelName string) bool
	// SubmitResultForUserApproval: cổng duyệt dùng chung cho mọi đường hoàn
	// thành task (xem hàm cùng tên). submitted = false thì hoàn thành như cũ.
	SubmitResultForUserApproval(ctx context.Context, team *store.TeamData, task *store.TeamTaskData, result string, actor TaskActor) (submitted bool, err error)
}

var _ TaskFeedbackService = (*TeamToolManager)(nil)

func (m *TeamToolManager) AnswerTask(ctx context.Context, team *store.TeamData, task *store.TeamTaskData, text string, assignee uuid.UUID, actor TaskActor) error {
	return AnswerTask(ctx, m, team, task, text, assignee, actor)
}

func (m *TeamToolManager) RequestTaskChanges(ctx context.Context, team *store.TeamData, task *store.TeamTaskData, reason string, actor TaskActor) error {
	return RequestTaskChanges(ctx, m, team, task, reason, actor)
}

func (m *TeamToolManager) ApproveUserCheckpoint(ctx context.Context, team *store.TeamData, task *store.TeamTaskData, actor TaskActor) error {
	return ApproveUserCheckpoint(ctx, m, team, task, actor)
}

// SetActionPolicy đặt policy hành động đang áp cho tool team_tasks. Gọi lúc
// khởi tạo, trước khi có dispatch.
func (m *TeamToolManager) SetActionPolicy(p TeamActionPolicy) { m.actionPolicy = p }

// actionAllowed cho biết hành động team_tasks có được policy hiện hành cho phép.
func (m *TeamToolManager) actionAllowed(action string) bool {
	if m.actionPolicy == nil {
		return true
	}
	return m.actionPolicy.IsAllowed(action)
}
