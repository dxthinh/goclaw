package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// acceptanceCriteriaExample đi kèm lỗi criteria để LLM sửa được ngay.
const acceptanceCriteriaExample = `Example: criteria=[{"index":1,"met":true,"evidence":"report.md covers North, South, East, West"},{"index":2,"met":false,"evidence":"2 numbers have no source yet"}]`

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

// forwardLeadReviewToUser xử lý lead approve trên task in_review có checkpoint
// "result" chưa tới lượt user (marker "lead" hoặc không marker): lead chỉ thông qua phần của mình, task chuyển sang chờ
// user duyệt (checkpoint_pending = "user") thay vì completed, để cổng duyệt của
// user không bị bỏ qua. Trạng thái giữ in_review; marker đổi bằng một câu
// UPDATE có điều kiện (in_review và marker vẫn như đã đọc), nên task đã được
// duyệt hoặc huỷ ở đường khác không bị gắn lại marker, metadata khác không mất.
func (t *TeamTasksTool) forwardLeadReviewToUser(ctx context.Context, team *store.TeamData, task *store.TeamTaskData, agentID uuid.UUID) *Result {
	err := t.manager.Store().TransitionTaskCheckpointPending(ctx, task.ID, team.ID, TaskCheckpointPending(task), CheckpointPendingUser)
	if errors.Is(err, store.ErrTaskCheckpointConflict) {
		return ErrorResult(fmt.Sprintf("task #%d is no longer waiting for your review (it was approved, rejected or changed elsewhere); check it with team_tasks(action=\"get\")", task.TaskNumber))
	}
	if err != nil {
		return ErrorResult("failed to forward the task to the user's approval: " + err.Error())
	}
	if task.Metadata == nil {
		task.Metadata = make(map[string]any)
	}
	task.Metadata[TaskMetaCheckpointPending] = CheckpointPendingUser
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

// reviewGateRejection trả lỗi (tiếng Anh, cho LLM) khi action review không được
// dùng cho task: task có contract ở team đã bật, hoặc task có checkpoint
// "result". Trả "" khi review được phép như cũ.
func reviewGateRejection(team *store.TeamData, task *store.TeamTaskData) string {
	switch {
	case GatedTaskContract(team, task) != nil:
		return fmt.Sprintf("Task #%d has a Task Contract: finish it with team_tasks(action=\"complete\", result=\"...\", criteria=[{\"index\": 1, \"met\": true, \"evidence\": \"...\"}, ...]) instead of review. If a criterion is not met, report it as met=false; the task then waits for the lead's review.", task.TaskNumber)
	case TaskCheckpoint(task) == TaskContractCheckpointResult:
		return fmt.Sprintf("Task #%d needs the user's approval of its result: finish it with team_tasks(action=\"complete\", result=\"...\") instead of review; the result is then sent to the user for approval.", task.TaskNumber)
	}
	return ""
}
