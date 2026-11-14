package tools

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// maxBlockedDependentsScan giới hạn số task active đọc khi tìm task bị chặn.
const maxBlockedDependentsScan = 200

// failDispatch đánh dấu task thất bại khi không dispatch được, phát event và báo lead.
// Trạng thái task trong bộ nhớ có thể đã cũ (caller vừa AssignTask), nên thử
// FailTask (in_progress) trước rồi FailPendingTask (pending/blocked).
func (m *TeamToolManager) failDispatch(ctx context.Context, task *store.TeamTaskData, team *store.TeamData, reason string) {
	if err := m.teamStore.FailTask(ctx, task.ID, team.ID, reason); err != nil {
		if err2 := m.teamStore.FailPendingTask(ctx, task.ID, team.ID, reason); err2 != nil {
			slog.Warn("team_tasks.dispatch: cannot mark task failed",
				"task_id", task.ID, "fail_err", err, "fail_pending_err", err2)
			return
		}
	}

	peerKind, _ := task.Metadata[TaskMetaPeerKind].(string)
	localKey, _ := task.Metadata[TaskMetaLocalKey].(string)
	m.broadcastTeamEvent(ctx, protocol.EventTeamTaskFailed, BuildTaskEventPayload(
		team.ID.String(), task.ID.String(),
		store.TeamTaskStatusFailed,
		"system", "dispatch",
		WithTaskInfo(task.TaskNumber, task.Subject),
		WithReason(reason),
		WithChannel(task.Channel),
		WithChatID(task.ChatID),
		WithPeerKind(peerKind),
		WithLocalKey(localKey),
	))

	leadAg, err := m.cachedGetAgentByID(ctx, team.LeadAgentID)
	if err != nil {
		slog.Warn("team_tasks.dispatch: cannot resolve lead to report failure", "task_id", task.ID, "error", err)
		return
	}
	content := fmt.Sprintf("[System] Task #%d \"%s\" (id: %s) could not be dispatched and was marked failed.\n\nReason: %s\n\n"+
		"Use team_tasks(action=\"retry\", task_id=\"%s\") after fixing the cause, or team_tasks(action=\"cancel\", task_id=\"%s\", text=\"reason\").",
		task.TaskNumber, task.Subject, task.ID, reason, task.ID, task.ID)
	content += BlockedDependentsNote(ctx, m.teamStore, team.ID, task.ID)

	// Kênh nội bộ không có phiên của lead: chuyển về dashboard như ticker.
	channel, chatID := task.Channel, task.ChatID
	if channel == "" || channel == ChannelSystem || channel == ChannelTeammate {
		channel, chatID = ChannelDashboard, team.ID.String()
	}
	meta := TaskLocalKeyMetadata(task)
	if meta == nil {
		meta = map[string]string{}
	}
	if s, ok := task.Metadata["origin_sender_id"].(string); ok && s != "" {
		meta[MetaOriginSenderID] = s
	}
	if !m.TryPublishInbound(bus.InboundMessage{
		Channel:  channel,
		SenderID: "system:escalation", // cùng đường giao ngay như escalation của blocker
		ChatID:   chatID,
		Metadata: meta,
		Content:  content,
		UserID:   team.CreatedBy,
		PeerKind: peerKind,
		TenantID: store.TenantIDFromContext(ctx),
		AgentID:  leadAg.AgentKey,
	}) {
		slog.Warn("team_tasks.dispatch: failure notice to lead dropped (bus unavailable or buffer full)",
			"task_id", task.ID, "lead", leadAg.AgentKey)
	}
}

// resetDispatchCount đặt bộ đếm dispatch về 0 trước một lần dispatch có chủ ý
// (retry, reject), để circuit breaker chỉ đếm các lần dispatch lại do lỗi.
func resetDispatchCount(task *store.TeamTaskData) {
	if task.Metadata == nil {
		task.Metadata = make(map[string]any)
	}
	task.Metadata["dispatch_count"] = 0
}

// ListBlockedDependents trả về các task đang blocked chờ taskID.
// Dùng ListTasks có sẵn rồi lọc BlockedBy trong Go, không thêm truy vấn store mới.
func ListBlockedDependents(ctx context.Context, teamStore store.TeamStore, teamID, taskID uuid.UUID) []store.TeamTaskData {
	if teamStore == nil {
		return nil
	}
	tasks, err := teamStore.ListTasks(ctx, teamID, "", store.TeamTaskFilterActive, "", "", "", maxBlockedDependentsScan, 0)
	if err != nil {
		slog.Warn("team_tasks: list blocked dependents failed", "task_id", taskID, "error", err)
		return nil
	}
	var out []store.TeamTaskData
	for _, t := range tasks {
		if t.Status == store.TeamTaskStatusBlocked && slices.Contains(t.BlockedBy, taskID) {
			out = append(out, t)
		}
	}
	return out
}

// BlockedDependentsNote tạo đoạn báo lead về các task vẫn bị chặn bởi một task
// thất bại. Trả chuỗi rỗng khi không có task nào.
func BlockedDependentsNote(ctx context.Context, teamStore store.TeamStore, teamID, taskID uuid.UUID) string {
	deps := ListBlockedDependents(ctx, teamStore, teamID, taskID)
	if len(deps) == 0 {
		return ""
	}
	lines := make([]string, 0, len(deps))
	for _, d := range deps {
		lines = append(lines, fmt.Sprintf("  - Task #%d (id: %s): %s", d.TaskNumber, d.ID, d.Subject))
	}
	return fmt.Sprintf("\n\n[Blocked dependents] These tasks stay blocked until this task is retried and completed, or cancelled:\n%s\n"+
		"Use team_tasks(action=\"retry\", task_id=\"%s\") to run it again, or team_tasks(action=\"cancel\", task_id=\"%s\", text=\"reason\") to release them.",
		strings.Join(lines, "\n"), taskID, taskID)
}

// BlockedDependentsNote là bản gắn với TeamToolManager.
func (m *TeamToolManager) BlockedDependentsNote(ctx context.Context, teamID, taskID uuid.UUID) string {
	return BlockedDependentsNote(ctx, m.teamStore, teamID, taskID)
}
