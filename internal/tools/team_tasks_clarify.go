package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// Loại câu hỏi của clarify.
const (
	ClarifyKindQuestion    = "question"
	ClarifyKindMissingTool = "missing_tool"
)

// executeClarify: member là owner hỏi lại lead. Task chuyển in_progress sang
// awaiting_input (không phải failed), câu hỏi được lưu thành comment, lead
// được báo và nhắc định kỳ được đặt. Member dừng và sẽ được dispatch lại khi
// lead (hoặc dashboard) trả lời bằng answer.
func (t *TeamTasksTool) executeClarify(ctx context.Context, args map[string]any) *Result {
	team, agentID, taskID, err := t.resolveTeamAndTask(ctx, args)
	if err != nil {
		return ErrorResult(err.Error())
	}
	text, _ := args["text"].(string)
	if err := validateFeedbackText(text); err != nil {
		return ErrorResult("clarify: " + err.Error() + " (your question for the lead)")
	}
	kind, _ := args["kind"].(string)
	switch kind {
	case "":
		kind = ClarifyKindQuestion
	case ClarifyKindQuestion, ClarifyKindMissingTool:
	default:
		return ErrorResult(fmt.Sprintf("kind must be %q or %q", ClarifyKindQuestion, ClarifyKindMissingTool))
	}

	task, err := t.manager.Store().GetTask(ctx, taskID)
	if err != nil {
		return ErrorResult("task not found: " + err.Error())
	}
	if task.TeamID != team.ID {
		return ErrorResult("task does not belong to your team")
	}
	if task.OwnerAgentID == nil || *task.OwnerAgentID != agentID {
		return ErrorResult("only the task owner can ask for clarification")
	}
	if task.Status != store.TeamTaskStatusInProgress {
		return ErrorResult(fmt.Sprintf("clarify only works on an in_progress task (current status: %s)", task.Status))
	}

	label := "Question"
	if kind == ClarifyKindMissingTool {
		label = "Missing tool"
	}
	if err := t.manager.Store().AddTaskComment(ctx, &store.TeamTaskCommentData{
		TaskID: taskID, AgentID: &agentID, Content: label + ": " + text, CommentType: "note",
	}); err != nil {
		return ErrorResult("failed to save question: " + err.Error())
	}

	// Nhắc định kỳ về chat gốc khi chưa có câu trả lời. SetTaskFollowup chỉ
	// nhận task in_progress nên phải đặt trước khi chuyển sang awaiting_input.
	followupSet := false
	switch task.Channel {
	case "", ChannelTeammate, ChannelSystem, ChannelDashboard:
	default:
		if task.ChatID != "" {
			at := time.Now().Add(time.Duration(t.manager.FollowupDelayMinutes(team)) * time.Minute)
			msg := fmt.Sprintf("Task #%d \"%s\" is waiting for an answer: %s", task.TaskNumber, task.Subject, text)
			if err := t.manager.Store().SetTaskFollowup(ctx, taskID, team.ID, at, t.manager.FollowupMaxReminders(team), msg, task.Channel, task.ChatID); err != nil {
				slog.Warn("clarify: set follow-up failed", "task_id", taskID, "error", err)
			} else {
				followupSet = true
			}
		}
	}

	if err := t.manager.Store().AwaitTaskInput(ctx, taskID, team.ID); err != nil {
		if followupSet {
			_ = t.manager.Store().ClearTaskFollowup(ctx, taskID)
		}
		return ErrorResult("failed to pause task for clarification: " + err.Error())
	}
	recordTaskAction(ctx, func(f *TaskActionFlags) { f.AwaitingInput = true })

	memberKey := t.manager.AgentKeyFromID(ctx, agentID)
	t.manager.BroadcastTeamEvent(ctx, protocol.EventTeamTaskUpdated, BuildTaskEventPayload(
		team.ID.String(), taskID.String(),
		store.TeamTaskStatusAwaitingInput,
		"agent", memberKey,
		WithTaskInfo(task.TaskNumber, task.Subject),
		WithOwnerAgentKey(memberKey),
		WithReason(truncatePreview(text, 500)),
		WithContextInfo(ctx),
	))

	var alternatives []string
	if kind == ClarifyKindMissingTool {
		alternatives = t.membersWithTaskNeeds(ctx, team, task, agentID)
	}
	notifyLeadOfFeedback(ctx, t.manager, team, task, buildClarifyLeadNotice(memberKey, task, taskID, kind, text, alternatives))

	return NewResult(fmt.Sprintf(
		"Your question was sent to the lead. Task #%d is now awaiting input. Stop working on it and end your turn now; "+
			"you will receive the task again together with the answer.", task.TaskNumber))
}

// buildClarifyLeadNotice dựng tin báo lead (tiếng Anh, dành cho LLM).
func buildClarifyLeadNotice(memberKey string, task *store.TeamTaskData, taskID uuid.UUID, kind, text string, alternatives []string) string {
	head := fmt.Sprintf("[Clarification needed] Member %q asked a question on task #%d \"%s\" (id: %s):\n\n%s\n\n",
		memberKey, task.TaskNumber, task.Subject, taskID, text)
	if kind == ClarifyKindMissingTool {
		head = fmt.Sprintf("[Missing tool] Member %q cannot do task #%d \"%s\" (id: %s) because a tool is missing:\n\n%s\n\n"+
			"Give the task to a member that has the tool with team_tasks(action=\"answer\", task_id=\"%s\", text=\"...\", assignee=\"<agent_key>\"), or cancel it.\n\n",
			memberKey, task.TaskNumber, task.Subject, taskID, text, taskID)
		if len(alternatives) > 0 {
			head += "Members that have every tool in the contract's needs: " + strings.Join(alternatives, ", ") + ".\n\n"
		}
	}
	return head + fmt.Sprintf(
		"Answer only if you know for certain: team_tasks(action=\"answer\", task_id=\"%s\", text=\"<answer>\"). "+
			"If you are not sure, ask the user in this chat first and answer after they reply. "+
			"Add assignee=\"<agent_key>\" to give the task to another member. "+
			"To drop the task: team_tasks(action=\"cancel\", task_id=\"%s\", text=\"reason\").",
		taskID, taskID)
}

// executeAnswer: lead trả lời câu hỏi clarify, task được dispatch lại cho
// owner hoặc cho assignee mới.
func (t *TeamTasksTool) executeAnswer(ctx context.Context, args map[string]any) *Result {
	team, agentID, taskID, err := t.resolveTeamAndTask(ctx, args)
	if err != nil {
		return ErrorResult(err.Error())
	}
	// Chỉ lead thật sự: không bỏ qua theo tên kênh như RequireLead, vì lượt
	// của member có thể chạy ở kênh teammate.
	if agentID != team.LeadAgentID {
		return ErrorResult("only the team lead can answer a clarification")
	}
	text, _ := args["text"].(string)

	task, err := t.manager.Store().GetTask(ctx, taskID)
	if err != nil {
		return ErrorResult("task not found: " + err.Error())
	}
	if task.TeamID != team.ID {
		return ErrorResult("task does not belong to your team")
	}

	assignee := uuid.Nil
	if key, _ := args["assignee"].(string); key != "" {
		id, err := t.manager.ResolveAgentByKey(ctx, key)
		if err != nil {
			return ErrorResult(fmt.Sprintf("assignee %q not found: %v", key, err))
		}
		members, err := t.manager.CachedListMembers(ctx, team.ID, agentID)
		if err != nil {
			return ErrorResult("failed to verify team membership: " + err.Error())
		}
		if !isTeamMember(members, id) {
			return ErrorResult(fmt.Sprintf("agent %q is not a member of this team", key))
		}
		assignee = id
	}

	actor := TaskActor{AgentID: &agentID, AgentKey: t.manager.AgentKeyFromID(ctx, agentID)}
	if err := AnswerTask(ctx, t.manager, team, task, text, assignee, actor); err != nil {
		return ErrorResult("answer failed: " + err.Error())
	}
	return NewResult(fmt.Sprintf("Answer recorded on task #%d \"%s\" and the task was dispatched to %s.",
		task.TaskNumber, task.Subject, t.manager.AgentKeyFromID(ctx, *task.OwnerAgentID)))
}

// membersWithTaskNeeds trả các member (trừ lead và owner) có đủ tool trong
// contract.needs khi owner báo thiếu tool, lấy từ cổng needs (TaskNeedsError).
// Rỗng khi task không có needs, owner vẫn đủ needs (tool thiếu nằm ngoài
// needs), team chưa bật contract hoặc backend không kiểm được.
func (t *TeamTasksTool) membersWithTaskNeeds(ctx context.Context, team *store.TeamData, task *store.TeamTaskData, ownerID uuid.UUID) []string {
	contract := TaskContractFromTask(task)
	if contract == nil || len(contract.Needs) == 0 {
		return nil
	}
	members, err := t.manager.CachedListMembers(ctx, team.ID, ownerID)
	if err != nil {
		return nil
	}
	var needsErr *TaskNeedsError
	if errors.As(checkTaskNeedsFromBackend(ctx, t.manager, team, members, contract, ownerID, ""), &needsErr) {
		return needsErr.Satisfying
	}
	return nil
}

// isTeamMember cho biết agentID có trong danh sách member của team.
func isTeamMember(members []store.TeamMemberData, agentID uuid.UUID) bool {
	for _, m := range members {
		if m.AgentID == agentID {
			return true
		}
	}
	return false
}
