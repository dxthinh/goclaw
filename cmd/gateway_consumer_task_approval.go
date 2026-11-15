package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// Giá trị Metadata["command"] do kênh phát cho lệnh duyệt task trong chat.
const (
	commandTaskApprove = "task_approve"
	commandTaskChanges = "task_changes"
)

// maxApprovalLookup là số task in_review tối đa đọc khi tìm task theo số.
const maxApprovalLookup = 200

// handleTaskApprovalCommand xử lý /approve và /changes trước agent, không qua
// LLM. Trả true khi tin nhắn là lệnh duyệt (đã xử lý, kể cả khi bị từ chối).
func handleTaskApprovalCommand(ctx context.Context, msg bus.InboundMessage, deps *ConsumerDeps) bool {
	cmd := msg.Metadata[tools.MetaCommand]
	if cmd != commandTaskApprove && cmd != commandTaskChanges {
		return false
	}
	reply := processTaskApprovalCommand(inboundMessageTenantContext(ctx, msg), msg, cmd, deps)
	if deps.MsgBus != nil && reply != "" {
		deps.MsgBus.PublishOutbound(bus.OutboundMessage{
			Channel:  msg.Channel,
			ChatID:   msg.ChatID,
			Content:  reply,
			Metadata: msg.Metadata,
		})
	}
	return true
}

// denyTaskApproval ghi log bảo mật và trả câu trả lời cho người gửi.
func denyTaskApproval(msg bus.InboundMessage, cmd, reason, reply string) string {
	slog.Warn("security.task_approval_denied",
		"reason", reason, "command", cmd, "channel", msg.Channel,
		"chat_id", msg.ChatID, "sender", msg.SenderID, "task_number", msg.Metadata["task_number"])
	return reply
}

// processTaskApprovalCommand kiểm tra theo thứ tự, dừng ở lỗi đầu tiên: kênh
// đã bật duyệt, người gửi không phải nội bộ, task tồn tại và đang in_review,
// task thuộc đúng chat, task chờ user duyệt, người gửi là người tạo yêu cầu
// hoặc file writer của nhóm. Trả câu trả lời gửi lại chat.
func processTaskApprovalCommand(ctx context.Context, msg bus.InboundMessage, cmd string, deps *ConsumerDeps) string {
	locale := store.LocaleFromContext(ctx)
	fb := deps.TaskFeedback
	if fb == nil || !fb.ChannelTaskApprovalEnabled(msg.Channel) {
		return denyTaskApproval(msg, cmd, "channel_not_enabled", i18n.T(locale, i18n.MsgTaskApprovalDisabled))
	}
	if msg.SenderID == "" || bus.IsInternalSender(msg.SenderID) {
		return denyTaskApproval(msg, cmd, "internal_sender", "")
	}
	num, err := strconv.Atoi(msg.Metadata["task_number"])
	if err != nil || num <= 0 {
		return i18n.T(locale, i18n.MsgTaskApprovalUsage)
	}
	notWaiting := i18n.T(locale, i18n.MsgTaskApprovalNotWaiting, num)
	if deps.TeamStore == nil || deps.AgentStore == nil {
		return denyTaskApproval(msg, cmd, "teams_unavailable", notWaiting)
	}

	agentKey := msg.AgentID
	if agentKey == "" {
		agentKey = resolveAgentRouteForInbound(ctx, deps.Cfg, deps.AgentStore, msg.Channel, msg.ChatID, msg.PeerKind)
	}
	ag, err := deps.AgentStore.GetByKey(ctx, agentKey)
	if err != nil || ag == nil {
		return denyTaskApproval(msg, cmd, "agent_not_found", notWaiting)
	}
	team, err := deps.TeamStore.GetTeamForAgent(ctx, ag.ID)
	if err != nil || team == nil {
		return denyTaskApproval(msg, cmd, "team_not_found", notWaiting)
	}
	task := findInReviewTaskByNumber(ctx, deps.TeamStore, team.ID, num)
	if task == nil {
		return denyTaskApproval(msg, cmd, "task_not_in_review", notWaiting)
	}
	if task.Channel != msg.Channel || task.ChatID != msg.ChatID {
		return denyTaskApproval(msg, cmd, "chat_mismatch", notWaiting)
	}
	if tools.TaskCheckpointPending(task) != tools.CheckpointPendingUser {
		return denyTaskApproval(msg, cmd, "not_user_checkpoint", notWaiting)
	}
	if !taskApprovalSenderAllowed(ctx, deps, msg, ag.ID, task) {
		return denyTaskApproval(msg, cmd, "sender_not_allowed",
			i18n.T(locale, i18n.MsgTaskApprovalSenderDenied, num))
	}

	actor := tools.TaskActor{UserID: senderNumericID(msg.SenderID)}
	if cmd == commandTaskApprove {
		if err := fb.ApproveUserCheckpoint(ctx, team, task, actor); err != nil {
			slog.Warn("task_approval: approve failed", "task_id", task.ID, "error", err)
			return i18n.T(locale, i18n.MsgTaskApprovalFailed, num, err.Error())
		}
		return i18n.T(locale, i18n.MsgTaskApprovalApproved, num, task.Subject)
	}
	if err := fb.RequestTaskChanges(ctx, team, task, msg.Metadata["text"], actor); err != nil {
		slog.Warn("task_approval: request changes failed", "task_id", task.ID, "error", err)
		return i18n.T(locale, i18n.MsgTaskApprovalFailed, num, err.Error())
	}
	return i18n.T(locale, i18n.MsgTaskApprovalChanges, num, task.Subject)
}

// findInReviewTaskByNumber tìm task in_review theo số trong team.
func findInReviewTaskByNumber(ctx context.Context, ts store.TeamStore, teamID uuid.UUID, num int) *store.TeamTaskData {
	tasks, err := ts.ListTasks(ctx, teamID, "", store.TeamTaskFilterInReview, "", "", "", maxApprovalLookup, 0)
	if err != nil {
		slog.Warn("task_approval: list in_review tasks failed", "team_id", teamID, "error", err)
		return nil
	}
	for i := range tasks {
		if tasks[i].TaskNumber == num && tasks[i].Status == store.TeamTaskStatusInReview {
			return &tasks[i]
		}
	}
	return nil
}

// senderNumericID lấy phần ID số của sender Telegram ("số|username").
func senderNumericID(senderID string) string {
	return strings.SplitN(senderID, "|", 2)[0]
}

// taskApprovalSenderAllowed: người gửi là người tạo yêu cầu (origin_sender_id
// của task, so phần ID số), hoặc là file writer của nhóm.
func taskApprovalSenderAllowed(ctx context.Context, deps *ConsumerDeps, msg bus.InboundMessage, agentID uuid.UUID, task *store.TeamTaskData) bool {
	sender := senderNumericID(msg.SenderID)
	if origin, _ := task.Metadata[tools.MetaOriginSenderID].(string); origin != "" && senderNumericID(origin) == sender {
		return true
	}
	if msg.PeerKind != "group" || deps.ConfigPermStore == nil {
		return false
	}
	groupID := fmt.Sprintf("group:%s:%s", msg.Channel, msg.ChatID)
	ok, err := deps.ConfigPermStore.CheckPermission(ctx, agentID, groupID, store.ConfigTypeFileWriter, sender)
	if err != nil {
		// Fail-closed: lỗi DB không được cấp quyền duyệt.
		slog.Warn("security.task_approval_writer_check_failed", "error", err, "sender", sender)
		return false
	}
	return ok
}
