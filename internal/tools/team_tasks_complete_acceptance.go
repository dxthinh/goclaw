package tools

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// acceptanceCriteriaExample đi kèm lỗi criteria để LLM sửa được ngay.
const acceptanceCriteriaExample = `Example: criteria=[{"index":1,"met":true,"evidence":"report.md covers North, South, East, West"},{"index":2,"met":false,"evidence":"2 numbers have no source yet"}]`

// saveAcceptanceReport ghi báo cáo tiêu chí vào metadata của task vừa
// completed. Lỗi chỉ ghi log: task đã hoàn thành, bảng tiêu chí đã nằm trong result.
func saveAcceptanceReport(ctx context.Context, ts store.TeamStore, task *store.TeamTaskData, extraMeta map[string]any) {
	if len(extraMeta) == 0 {
		return
	}
	if task.Metadata == nil {
		task.Metadata = make(map[string]any, len(extraMeta))
	}
	for k, v := range extraMeta {
		task.Metadata[k] = v
	}
	if err := ts.UpdateTask(ctx, task.ID, map[string]any{"metadata": task.Metadata}); err != nil {
		slog.Warn("team_tasks.complete: save acceptance report failed", "task_id", task.ID, "error", err)
	}
}

// broadcastLeadReview phát EventTeamTaskReviewed cho task vừa chuyển sang chờ lead xem.
func (t *TeamTasksTool) broadcastLeadReview(ctx context.Context, team *store.TeamData, task *store.TeamTaskData, agentID uuid.UUID) {
	ownerKey := t.manager.AgentKeyFromID(ctx, agentID)
	t.manager.BroadcastTeamEvent(ctx, protocol.EventTeamTaskReviewed, BuildTaskEventPayload(
		team.ID.String(), task.ID.String(),
		store.TeamTaskStatusInReview,
		"agent", ownerKey,
		WithTaskInfo(task.TaskNumber, task.Subject),
		WithOwner(ownerKey, t.manager.AgentDisplayName(ctx, ownerKey)),
		WithContextInfo(ctx),
		WithLocalKey(taskLocalKey(task)),
	))
}

// leadReviewCompleteMessage là kết quả của complete khi task chuyển sang chờ
// lead xem (tiếng Anh, cho LLM). callerIsLead: lead tự gọi complete thì được
// nhắc cách duyệt hoặc trả lại.
func leadReviewCompleteMessage(task *store.TeamTaskData, chk AcceptanceCheck, callerIsLead bool) string {
	var why []string
	if len(chk.Missing) > 0 {
		why = append(why, "acceptance criteria not reported: "+joinInts(chk.Missing))
	}
	if len(chk.Unmet) > 0 {
		why = append(why, "acceptance criteria not met: "+joinInts(chk.Unmet))
	}
	msg := fmt.Sprintf("Task #%d did not complete: %s. Its result is now waiting for the lead's review (status in_review).", task.TaskNumber, strings.Join(why, "; "))
	if callerIsLead {
		return msg + fmt.Sprintf(" You are the lead: accept it with team_tasks(action=\"approve\", task_id=\"%s\") or send it back with team_tasks(action=\"reject\", task_id=\"%s\", text=\"<what to fix>\").", task.ID, task.ID)
	}
	return msg + " The lead will accept it or send it back with feedback. Your work on it is done — end your turn."
}

// LeadReviewNotice là dòng đầu của tin báo lead khi task có contract đang chờ
// lead xem (tiếng Anh, cho LLM). noComplete: lượt chạy kết thúc mà member
// không gọi complete.
func LeadReviewNotice(task *store.TeamTaskData, noComplete bool) string {
	reason := "the member reported that not every acceptance criterion is met"
	if noComplete {
		reason = "the member's run ended without calling complete"
	}
	return fmt.Sprintf("[Task #%d waits for your review] %s, so the task is not completed. "+
		"Accept the result with team_tasks(action=\"approve\", task_id=\"%s\"), or send it back with team_tasks(action=\"reject\", task_id=\"%s\", text=\"<what to fix>\").",
		task.TaskNumber, reason, task.ID, task.ID)
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return strings.Join(parts, ", ")
}

// forwardLeadReviewToUser xử lý lead approve trên task chờ lead xem mà có
// checkpoint "result": lead chỉ thông qua phần của mình, task chuyển sang chờ
// user duyệt (checkpoint_pending = "user") thay vì completed, để cổng duyệt của
// user không bị bỏ qua. Trạng thái giữ in_review; chỉ marker đổi trong một câu UPDATE.
func (t *TeamTasksTool) forwardLeadReviewToUser(ctx context.Context, team *store.TeamData, task *store.TeamTaskData, agentID uuid.UUID) *Result {
	if task.Status != store.TeamTaskStatusInReview {
		return ErrorResult(fmt.Sprintf("task #%d is %s, not in_review", task.TaskNumber, task.Status))
	}
	if task.Metadata == nil {
		task.Metadata = make(map[string]any)
	}
	task.Metadata[TaskMetaCheckpointPending] = CheckpointPendingUser
	if err := t.manager.Store().UpdateTask(ctx, task.ID, map[string]any{"metadata": task.Metadata}); err != nil {
		return ErrorResult("failed to forward the task to the user's approval: " + err.Error())
	}
	_ = t.manager.Store().AddTaskComment(ctx, &store.TeamTaskCommentData{
		TaskID: task.ID, AgentID: &agentID,
		Content: "Lead accepted the result; waiting for the user's approval.",
	})
	t.broadcastLeadReview(ctx, team, task, agentID)
	if t.manager.ChannelTaskApprovalEnabled(task.Channel) {
		t.manager.NotifyUserApproval(ctx, task)
	}
	return NewResult(fmt.Sprintf("Task #%d needs the user's approval of its result, so it is not completed yet. "+
		"It now waits for the user: /approve %d in the original chat, or the dashboard.", task.TaskNumber, task.TaskNumber))
}
