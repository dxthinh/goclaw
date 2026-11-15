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

// TaskMetaCheckpoint lưu checkpoint của task KHÔNG có contract (require_approval
// trên task không contract). Task có contract dùng contract.checkpoint. Luôn đọc
// qua TaskCheckpoint, không đọc trực tiếp.
const TaskMetaCheckpoint = "checkpoint"

// TaskCheckpoint trả checkpoint hiện hành của task ("" hoặc "result"): ưu tiên
// contract.checkpoint, nếu task không có contract thì đọc metadata.checkpoint.
// Mọi chỗ kiểm tra cổng duyệt kết quả của user phải gọi hàm này.
func TaskCheckpoint(task *store.TeamTaskData) string {
	if task == nil {
		return ""
	}
	if c := TaskContractFromTask(task); c != nil {
		return c.Checkpoint
	}
	if task.Metadata == nil {
		return ""
	}
	if v, _ := task.Metadata[TaskMetaCheckpoint].(string); v == TaskContractCheckpointResult {
		return v
	}
	return ""
}

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

// userApprovalBackend là phần backend mà bước nộp kết quả chờ user duyệt cần.
type userApprovalBackend interface {
	Store() store.TeamStore
	BroadcastTeamEvent(ctx context.Context, name string, payload any)
	ChannelTaskApprovalEnabled(channelName string) bool
	NotifyUserApproval(ctx context.Context, task *store.TeamTaskData)
}

// SubmitResultForUserApproval là cổng duyệt của mọi đường hoàn thành task
// (tool complete, tự hoàn thành sau lượt chạy). Task có checkpoint "result"
// (đọc qua TaskCheckpoint, cả task có lẫn không có contract) không thành
// completed: chuyển in_progress → in_review kèm result, đặt
// checkpoint_pending = "user", phát EventTeamTaskReviewed và gửi tin chờ duyệt
// về chat gốc. Task phụ thuộc không được mở khóa vì task chưa completed.
// Trả submitted = false khi task không có checkpoint; người gọi hoàn thành
// task như cũ.
func SubmitResultForUserApproval(ctx context.Context, b userApprovalBackend, team *store.TeamData, task *store.TeamTaskData, result string, actor TaskActor) (submitted bool, err error) {
	if TaskCheckpoint(task) != TaskContractCheckpointResult {
		return false, nil
	}
	// Trạng thái in_review, result và marker checkpoint_pending ghi trong cùng
	// một câu UPDATE: lỗi thì task vẫn in_progress, không kẹt in_review thiếu
	// marker (submit lại được, sweeper không đánh stale nhầm).
	patch := map[string]any{TaskMetaCheckpointPending: CheckpointPendingUser}
	if err := b.Store().SubmitTaskResultForReview(ctx, task.ID, team.ID, result, patch); err != nil {
		return true, fmt.Errorf("failed to submit result for the user's approval: %w", err)
	}
	if task.Metadata == nil {
		task.Metadata = make(map[string]any)
	}
	task.Metadata[TaskMetaCheckpointPending] = CheckpointPendingUser
	task.Status = store.TeamTaskStatusInReview
	b.BroadcastTeamEvent(ctx, protocol.EventTeamTaskReviewed, BuildTaskEventPayload(
		team.ID.String(), task.ID.String(),
		store.TeamTaskStatusInReview,
		actor.actorType(), actor.actorID(),
		WithTaskInfo(task.TaskNumber, task.Subject),
		WithChannel(task.Channel),
		WithChatID(task.ChatID),
		WithLocalKey(taskLocalKey(task)),
	))
	// Tin chờ duyệt chỉ gửi khi kênh gốc còn duyệt được bằng lệnh; nếu kênh đã
	// tắt task_approval, task vẫn chờ và được duyệt trên dashboard.
	if b.ChannelTaskApprovalEnabled(task.Channel) {
		b.NotifyUserApproval(ctx, task)
	} else {
		slog.Warn("task_approval: origin channel no longer approves by command; task waits for dashboard approval",
			"task_id", task.ID, "channel", task.Channel)
	}
	return true, nil
}

func (m *TeamToolManager) SubmitResultForUserApproval(ctx context.Context, team *store.TeamData, task *store.TeamTaskData, result string, actor TaskActor) (bool, error) {
	return SubmitResultForUserApproval(ctx, m, team, task, result, actor)
}
