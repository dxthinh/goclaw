package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// Khóa metadata checkpoint_pending (contract.md mục 3): task in_review đang
// chờ ai duyệt. "user": chỉ lệnh trong chat gốc hoặc dashboard duyệt được,
// agent bị chặn. "lead": lead duyệt. Ticker không đánh stale khi có giá trị.
const (
	TaskMetaCheckpointPending = "checkpoint_pending"
	CheckpointPendingUser     = "user"
	CheckpointPendingLead     = "lead"
)

// SenderTaskFeedback là sender của tin hệ thống báo lead về clarify/duyệt.
// Có tiền tố "system:" nên là sender nội bộ và đi thẳng, không qua debounce.
const SenderTaskFeedback = "system:task_feedback"

// TaskCheckpointPending đọc metadata.checkpoint_pending của task ("" khi không có).
func TaskCheckpointPending(task *store.TeamTaskData) string {
	if task == nil || task.Metadata == nil {
		return ""
	}
	v, _ := task.Metadata[TaskMetaCheckpointPending].(string)
	return v
}

// clearCheckpointPending xóa checkpoint_pending khỏi metadata (chưa ghi DB).
func clearCheckpointPending(task *store.TeamTaskData) {
	if task.Metadata != nil {
		delete(task.Metadata, TaskMetaCheckpointPending)
	}
}

// userCheckpointRejection là lỗi trả cho agent khi gọi approve/reject trên task
// đang chờ user duyệt. Lớp chặn này nằm trong mã của tool, không trong hook.
func userCheckpointRejection(task *store.TeamTaskData) string {
	return fmt.Sprintf("Task #%d is waiting for the user's approval. Agents cannot approve or reject it. "+
		"The user approves with /approve %d or requests changes with /changes %d <reason> in the original chat, or on the dashboard.",
		task.TaskNumber, task.TaskNumber, task.TaskNumber)
}

// unblockedDispatcher là backend dispatch được task phụ thuộc (*TeamToolManager).
type unblockedDispatcher interface {
	DispatchUnblockedTasks(ctx context.Context, teamID uuid.UUID)
}

// ApproveUserCheckpoint nhận kết quả của task đang chờ user duyệt: chuyển
// in_review thành completed, xóa checkpoint_pending, phát sự kiện, dispatch
// task phụ thuộc và báo lead. Người gọi đã kiểm tra quyền của người duyệt.
func ApproveUserCheckpoint(ctx context.Context, b taskFeedbackBackend, team *store.TeamData, task *store.TeamTaskData, actor TaskActor) error {
	if task.Status != store.TeamTaskStatusInReview {
		return ErrTaskNotInReview
	}
	if err := b.Store().ApproveTask(ctx, task.ID, team.ID, ""); err != nil {
		return fmt.Errorf("failed to approve task: %w", err)
	}
	// Xóa khóa sau khi đã completed: nếu xóa trước mà approve lỗi, task
	// in_review không còn khóa sẽ bị ticker đánh stale.
	clearCheckpointPending(task)
	if err := b.Store().UpdateTask(ctx, task.ID, map[string]any{"metadata": task.Metadata}); err != nil {
		slog.Warn("task_approval: clear checkpoint_pending failed", "task_id", task.ID, "error", err)
	}
	task.Status = store.TeamTaskStatusCompleted
	_ = b.Store().AddTaskComment(ctx, &store.TeamTaskCommentData{
		TaskID: task.ID, AgentID: actor.AgentID, UserID: actor.UserID,
		Content: "Result approved by the user.",
	})
	b.BroadcastTeamEvent(ctx, protocol.EventTeamTaskApproved, BuildTaskEventPayload(
		team.ID.String(), task.ID.String(),
		store.TeamTaskStatusCompleted,
		actor.actorType(), actor.actorID(),
		WithTaskInfo(task.TaskNumber, task.Subject),
		WithChannel(task.Channel),
		WithChatID(task.ChatID),
		WithLocalKey(taskLocalKey(task)),
	))
	if d, ok := b.(unblockedDispatcher); ok {
		d.DispatchUnblockedTasks(ctx, team.ID)
	}
	if p, ok := b.(inboundPublisher); ok {
		notifyLeadOfFeedback(ctx, p, team, task, fmt.Sprintf(
			"[Task approved] The user approved the result of task #%d \"%s\". It is now completed; dependent tasks have been dispatched.",
			task.TaskNumber, task.Subject))
	}
	return nil
}

// NotifyUserApproval gửi tin "task chờ bạn duyệt" về đúng chat gốc của task
// (kèm local_key cho forum topic), nêu số task và cú pháp lệnh duyệt.
func (m *TeamToolManager) NotifyUserApproval(ctx context.Context, task *store.TeamTaskData) {
	if m.msgBus == nil || task == nil || task.Channel == "" || task.ChatID == "" {
		return
	}
	content := i18n.T(store.LocaleFromContext(ctx), i18n.MsgTaskApprovalPrompt,
		task.TaskNumber, task.Subject, task.TaskNumber, task.TaskNumber)
	m.msgBus.PublishOutbound(reviewOutboundMessage(task, content))
}

// inboundPublisher là backend gửi được tin inbound tới agent (lead).
type inboundPublisher interface {
	TryPublishInbound(msg bus.InboundMessage) bool
	CachedGetAgentByID(ctx context.Context, id uuid.UUID) (*store.AgentData, error)
}

// notifyLeadOfFeedback gửi một tin hệ thống tới lead trong chat gốc của task,
// theo mẫu báo blocker. Lỗi chỉ được ghi log: thao tác chính đã xong.
func notifyLeadOfFeedback(ctx context.Context, p inboundPublisher, team *store.TeamData, task *store.TeamTaskData, content string) {
	leadAg, err := p.CachedGetAgentByID(ctx, team.LeadAgentID)
	if err != nil {
		slog.Warn("task_feedback: cannot resolve lead", "team_id", team.ID, "error", err)
		return
	}
	meta := TaskLocalKeyMetadata(task)
	if meta == nil {
		meta = map[string]string{}
	}
	if s, ok := task.Metadata["origin_sender_id"].(string); ok && s != "" && !bus.IsInternalSender(s) {
		meta[MetaOriginSenderID] = s
	}
	peerKind, _ := task.Metadata[TaskMetaPeerKind].(string)
	if !p.TryPublishInbound(bus.InboundMessage{
		Channel:  task.Channel,
		SenderID: SenderTaskFeedback,
		ChatID:   task.ChatID,
		Metadata: meta,
		Content:  content,
		UserID:   task.UserID,
		PeerKind: peerKind,
		TenantID: store.TenantIDFromContext(ctx),
		AgentID:  leadAg.AgentKey,
	}) {
		slog.Warn("task_feedback: lead notice dropped (bus unavailable or buffer full)",
			"task_id", task.ID, "leader", leadAg.AgentKey)
	}
}
