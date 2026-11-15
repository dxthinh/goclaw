package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func (t *TeamTasksTool) executeAskUser(ctx context.Context, args map[string]any) *Result {
	team, agentID, taskID, err := t.resolveTeamAndTask(ctx, args)
	if err != nil {
		return ErrorResult(err.Error())
	}

	followupMessage, _ := args["text"].(string)
	if followupMessage == "" {
		return ErrorResult("text is required for ask_user action (the question for the user)")
	}

	// Verify ownership.
	task, err := t.manager.Store().GetTask(ctx, taskID)
	if err != nil {
		return ErrorResult("task not found: " + err.Error())
	}
	if task.TeamID != team.ID {
		return ErrorResult("task does not belong to your team")
	}
	if task.OwnerAgentID == nil || *task.OwnerAgentID != agentID {
		return ErrorResult("only the task owner can set follow-up reminders")
	}

	// Resolve delay and max from team settings.
	delayMinutes := t.manager.FollowupDelayMinutes(team)
	maxReminders := t.manager.FollowupMaxReminders(team)

	// Resolve channel: prefer task's channel, fallback to context channel.
	channel := task.Channel
	chatID := task.ChatID
	ctxChannel := OriginChannelFromCtx(ctx)
	if channel == "" || channel == ChannelTeammate || channel == ChannelSystem || channel == ChannelDashboard {
		channel = ctxChannel
		chatID = OriginChatIDFromCtx(ctx)
	}
	if channel == "" || channel == ChannelTeammate || channel == ChannelSystem || channel == ChannelDashboard {
		return ErrorResult("cannot set follow-up: no valid channel found (task has no origin channel and context channel is internal)")
	}

	followupAt := time.Now().Add(time.Duration(delayMinutes) * time.Minute)
	if err := t.manager.Store().SetTaskFollowup(ctx, taskID, team.ID, followupAt, maxReminders, followupMessage, channel, chatID); err != nil {
		return ErrorResult("failed to set follow-up: " + err.Error())
	}

	maxDesc := "unlimited"
	if maxReminders > 0 {
		maxDesc = fmt.Sprintf("max %d", maxReminders)
	}
	return NewResult(fmt.Sprintf("Follow-up set for task %s. First reminder in %d minutes via %s (%s).", taskID, delayMinutes, channel, maxDesc))
}

func (t *TeamTasksTool) executeClearAskUser(ctx context.Context, args map[string]any) *Result {
	team, agentID, taskID, err := t.resolveTeamAndTask(ctx, args)
	if err != nil {
		return ErrorResult(err.Error())
	}

	// Verify task belongs to team.
	task, err := t.manager.Store().GetTask(ctx, taskID)
	if err != nil {
		return ErrorResult("task not found: " + err.Error())
	}
	if task.TeamID != team.ID {
		return ErrorResult("task does not belong to your team")
	}
	// Allow owner or lead to clear.
	if task.OwnerAgentID == nil || (*task.OwnerAgentID != agentID && agentID != team.LeadAgentID) {
		return ErrorResult("only the task owner or team lead can clear follow-up reminders")
	}

	if err := t.manager.Store().ClearTaskFollowup(ctx, taskID); err != nil {
		return ErrorResult("failed to clear follow-up: " + err.Error())
	}

	return NewResult(fmt.Sprintf("Follow-up reminders cleared for task %s.", taskID))
}

func (t *TeamTasksTool) executeRetry(ctx context.Context, args map[string]any) *Result {
	team, agentID, taskID, err := t.resolveTeamAndTask(ctx, args)
	if err != nil {
		return ErrorResult(err.Error())
	}
	if err := t.manager.RequireLead(ctx, team, agentID); err != nil {
		return ErrorResult(err.Error())
	}

	task, err := t.manager.Store().GetTask(ctx, taskID)
	if err != nil {
		return ErrorResult("task not found: " + err.Error())
	}
	if task.TeamID != team.ID {
		return ErrorResult("task does not belong to your team")
	}
	switch task.Status {
	case store.TeamTaskStatusStale, store.TeamTaskStatusFailed, store.TeamTaskStatusCompleted:
		// OK — can retry/reopen these statuses
	default:
		return ErrorResult(fmt.Sprintf("retry only works on completed, stale, or failed tasks (current status: %s)", task.Status))
	}
	if task.OwnerAgentID == nil {
		return ErrorResult("task has no assignee — assign it first via update")
	}
	// Block retry to the lead agent — would cause self-dispatch loop.
	if *task.OwnerAgentID == team.LeadAgentID {
		return ErrorResult("cannot retry task assigned to the team lead — reassign to a team member first via update")
	}
	// Team bắt buộc contract: không dispatch lại task loại bắt buộc chưa có contract hợp lệ.
	if err := CheckTaskContractForAssign(team, task); err != nil {
		return ErrorResult("cannot retry: " + err.Error())
	}
	if contract := TaskContractFromTask(task); contract != nil && len(contract.Needs) > 0 {
		members, err := t.manager.CachedListMembers(ctx, team.ID, agentID)
		if err != nil {
			return ErrorResult("failed to load team members: " + err.Error())
		}
		if err := checkTaskNeedsFromBackend(ctx, t.manager, team, members, contract, *task.OwnerAgentID, taskChannelTypeFromCtx(ctx, task.Channel)); err != nil {
			return ErrorResult("cannot retry: " + err.Error())
		}
	}

	// Reset status to pending first (AssignTask only transitions from pending).
	if err := t.manager.Store().ResetTaskStatus(ctx, taskID, team.ID); err != nil {
		return ErrorResult("failed to reset task: " + err.Error())
	}
	// Assign (pending → in_progress + lock).
	if err := t.manager.Store().AssignTask(ctx, taskID, *task.OwnerAgentID, team.ID); err != nil {
		return ErrorResult("failed to retry task: " + err.Error())
	}

	t.manager.BroadcastTeamEvent(ctx, protocol.EventTeamTaskDispatched, BuildTaskEventPayload(
		team.ID.String(), taskID.String(),
		store.TeamTaskStatusInProgress,
		"agent", t.manager.AgentKeyFromID(ctx, agentID),
		WithTaskInfo(task.TaskNumber, task.Subject),
		WithOwnerAgentKey(t.manager.AgentKeyFromID(ctx, *task.OwnerAgentID)),
		WithContextInfo(ctx),
	))

	// Dispatch immediately (retry is an explicit action, not during a turn).
	// Dispatch có chủ ý: đặt lại bộ đếm để circuit breaker chỉ tính lần lỗi.
	resetDispatchCount(task)
	t.manager.DispatchTaskToAgent(ctx, task, team, *task.OwnerAgentID)

	assignee := t.manager.AgentKeyFromID(ctx, *task.OwnerAgentID)
	return NewResult(fmt.Sprintf("Task #%d \"%s\" (id: %s) retried and dispatched to %s. The assignee will receive the task with your recent comments.", task.TaskNumber, task.Subject, taskID, assignee))
}
