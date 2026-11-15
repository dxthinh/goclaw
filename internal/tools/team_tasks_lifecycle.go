package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

func (t *TeamTasksTool) executeClaim(ctx context.Context, args map[string]any) *Result {
	team, agentID, taskID, err := t.resolveTeamAndTask(ctx, args)
	if err != nil {
		return ErrorResult(err.Error())
	}

	// Claim là member tự nhận task: cùng cổng needs như khi giao, claimant là assignee.
	if msg := t.checkClaimNeeds(ctx, team, agentID, taskID); msg != "" {
		return ErrorResult(msg)
	}
	if err := t.manager.Store().ClaimTask(ctx, taskID, agentID, team.ID); err != nil {
		return ErrorResult("failed to claim task: " + err.Error())
	}
	// Record action flag after successful store operation.
	recordTaskAction(ctx, func(f *TaskActionFlags) { f.Claimed = true })

	ownerKey := t.manager.AgentKeyFromID(ctx, agentID)
	t.manager.BroadcastTeamEvent(ctx, protocol.EventTeamTaskClaimed, BuildTaskEventPayload(
		team.ID.String(), taskID.String(),
		store.TeamTaskStatusInProgress,
		"agent", ownerKey,
		WithOwner(ownerKey, t.manager.AgentDisplayName(ctx, ownerKey)),
		WithContextInfo(ctx),
	))

	return NewResult(fmt.Sprintf("Task %s claimed successfully. It is now in progress.", taskID))
}

func (t *TeamTasksTool) executeComplete(ctx context.Context, args map[string]any) *Result {
	// Note: reviewer role exists — when task has a reviewer, executeComplete
	// routes to in_review status instead of completed. Leader still approves.
	// Member agents always call this; leader bypasses via direct status update.

	team, agentID, taskID, err := t.resolveTeamAndTask(ctx, args)
	if err != nil {
		return ErrorResult(err.Error())
	}

	result, _ := args["result"].(string)
	if result == "" {
		return ErrorResult("result is required for complete action")
	}

	// Validate before completing: a wrong path must stay fixable by the model,
	// and a completed task cannot be completed again with the right one.
	deliverables, err := resolveTaskDeliverables(ctx, args["deliverables"])
	if err != nil {
		return ErrorResult(err.Error())
	}
	if len(deliverables) > 0 {
		if err := checkDeliverablesInTaskRun(ctx, taskID); err != nil {
			return ErrorResult(err.Error())
		}
	}

	// Chỉ owner hoặc lead được complete. Lấy task trước khi claim; bản này dùng
	// lại cho event bên dưới nên không thêm truy vấn.
	task, err := t.manager.Store().GetTask(ctx, taskID)
	if err != nil {
		return ErrorResult("task not found: " + err.Error())
	}
	if task.TeamID != team.ID {
		return ErrorResult("task does not belong to your team")
	}
	if ch := ToolChannelFromCtx(ctx); task.OwnerAgentID != nil && *task.OwnerAgentID != agentID &&
		agentID != team.LeadAgentID && ch != ChannelSystem && ch != ChannelDashboard {
		return ErrorResult(fmt.Sprintf("task %s is assigned to %s — only its owner or the team lead can complete it",
			taskID, t.manager.AgentKeyFromID(ctx, *task.OwnerAgentID)))
	}

	// Team bật contract và task có contract: đọc báo cáo tiêu chí trước khi
	// đổi trạng thái, để báo cáo sai vẫn sửa được bằng một lần gọi lại.
	contract := GatedTaskContract(team, task)
	var acceptance *AcceptanceCheck
	var extraMeta map[string]any
	if contract != nil {
		criteria, err := ParseAcceptanceCriteria(args["criteria"], len(contract.Acceptance))
		if err != nil {
			return ErrorResult(err.Error() + ". " + acceptanceCriteriaExample)
		}
		chk := CheckAcceptance(contract, criteria)
		acceptance = &chk
		extraMeta = map[string]any{TaskMetaAcceptanceReport: acceptanceReportMetadata(criteria)}
		if table := FormatAcceptanceReport(contract, criteria); table != "" {
			result += "\n\n" + table
		}
	}

	// Auto-claim if the task is still pending (saves an extra tool call).
	// ClaimTask is atomic — only one agent can succeed, others get an error.
	// Ignore claim error: task may already be in_progress (claimed by us or someone else).
	_ = t.manager.Store().ClaimTask(ctx, taskID, agentID, team.ID)

	if len(deliverables) > 0 {
		if err := t.checkDeliverablesAllowed(ctx, team, taskID, agentID); err != nil {
			return ErrorResult(err.Error())
		}
	}

	// Mọi lần hoàn thành đi qua cổng duyệt của user: task có checkpoint
	// "result" được nộp chờ duyệt (in_review) thay vì completed.
	actor := TaskActor{AgentID: &agentID, AgentKey: t.manager.AgentKeyFromID(ctx, agentID)}
	submitted, leadReview := false, false
	finish := func() error {
		// Thiếu hoặc có tiêu chí chưa đạt: chờ lead xem, kể cả khi task có
		// checkpoint "result" (lead duyệt xong mới tới lượt user).
		if acceptance != nil && acceptance.Verdict != AcceptanceAllMet {
			if err := SubmitResultForLeadReview(ctx, t.manager.Store(), team, task, result, extraMeta); err != nil {
				return err
			}
			leadReview = true
			return nil
		}
		ok, err := submitResultForUserApproval(ctx, t.manager, team, task, result, actor, extraMeta)
		if ok {
			submitted = err == nil
			return err
		}
		// Báo cáo tiêu chí (nếu có) ghi trong cùng câu UPDATE hoàn thành task.
		return t.manager.Store().CompleteTaskWithMetadata(ctx, taskID, team.ID, result, extraMeta)
	}

	if err := finish(); err != nil {
		// Fast path: check in-memory turn flags before hitting DB again.
		if flags := TaskActionFlagsFromCtx(ctx); flags != nil && flags.Completed {
			return NewResult(fmt.Sprintf("Task %s already completed.%s", taskID, t.registerDeliverables(ctx, team.ID, taskID, agentID, deliverables)))
		}
		// Slow path: re-query to determine actual status.
		if current, getErr := t.manager.Store().GetTask(ctx, taskID); getErr == nil && current != nil {
			switch current.Status {
			case store.TeamTaskStatusCompleted:
				recordTaskAction(ctx, func(f *TaskActionFlags) { f.Completed = true })
				return NewResult(fmt.Sprintf("Task %s already completed.%s", taskID, t.registerDeliverables(ctx, team.ID, taskID, agentID, deliverables)))
			case store.TeamTaskStatusFailed, store.TeamTaskStatusCancelled:
				return NewResult(fmt.Sprintf("Task %s is %s — cannot complete.", taskID, current.Status))
			case store.TeamTaskStatusInReview:
				switch TaskCheckpointPending(current) {
				case CheckpointPendingUser:
					recordTaskAction(ctx, func(f *TaskActionFlags) { f.Completed = true })
					return NewResult(fmt.Sprintf("Task %s is already waiting for the user's approval.", taskID))
				case CheckpointPendingLead:
					recordTaskAction(ctx, func(f *TaskActionFlags) { f.Completed = true })
					return NewResult(fmt.Sprintf("Task %s is already waiting for the lead's review.", taskID))
				}
			case store.TeamTaskStatusPending:
				// Task was reset by stale recovery — re-assign and retry once.
				if t.manager.Store().AssignTask(ctx, taskID, agentID, team.ID) == nil {
					if finish() == nil {
						slog.Info("executeComplete: re-assigned stale-recovered task", "task_id", taskID)
						err = nil
					}
				}
			}
		}
		if err != nil {
			return ErrorResult("failed to complete task: " + err.Error())
		}
	}
	// Record action flag after successful store operation (cả khi nộp chờ duyệt:
	// bước sau lượt chạy không được tự hoàn thành task).
	recordTaskAction(ctx, func(f *TaskActionFlags) { f.Completed = true })
	registered := t.registerDeliverables(ctx, team.ID, taskID, agentID, deliverables)
	if leadReview {
		t.broadcastLeadReview(ctx, team, task, agentID)
		return NewResult(leadReviewCompleteMessage(task, *acceptance, agentID == team.LeadAgentID) + registered)
	}
	if submitted {
		return NewResult(fmt.Sprintf("Task #%d result submitted for the user's approval.%s The task completes only when the user approves it; dependent tasks wait until then. Your work on it is done — end your turn.", task.TaskNumber, registered))
	}

	ownerKey := t.manager.AgentKeyFromID(ctx, agentID)
	// TaskNumber/Subject/local_key cho subscriber thông báo lấy từ task đã đọc ở trên.
	taskLocalKey, _ := task.Metadata[TaskMetaLocalKey].(string)
	t.manager.BroadcastTeamEvent(ctx, protocol.EventTeamTaskCompleted, BuildTaskEventPayload(
		team.ID.String(), taskID.String(),
		store.TeamTaskStatusCompleted,
		"agent", ownerKey,
		WithTaskInfo(task.TaskNumber, task.Subject),
		WithOwner(ownerKey, t.manager.AgentDisplayName(ctx, ownerKey)),
		WithContextInfo(ctx),
		WithLocalKey(taskLocalKey),
	))

	// Dependent tasks are dispatched by the consumer after this agent's turn ends
	// (post-turn), not mid-turn. This prevents dependent tasks from completing and
	// announcing to the leader before this agent's own run finishes.

	return NewResult(fmt.Sprintf("Task %s completed.%s Dependent tasks will be dispatched after this turn ends.", taskID, registered))
}

func (t *TeamTasksTool) executeCancel(ctx context.Context, args map[string]any) *Result {
	// Note: reviewer role not yet active. Cancellation goes through leader only.

	team, agentID, taskID, err := t.resolveTeamAndTask(ctx, args)
	if err != nil {
		return ErrorResult(err.Error())
	}
	if err := t.manager.RequireLead(ctx, team, agentID); err != nil {
		return ErrorResult(err.Error())
	}

	reason, _ := args["text"].(string)
	if reason == "" {
		reason = "Cancelled by agent"
	}

	// CancelTask: guards against completed tasks, unblocks dependents, transitions blocked→pending.
	if err := t.manager.Store().CancelTask(ctx, taskID, team.ID, reason); err != nil {
		return ErrorResult("failed to cancel task: " + err.Error())
	}

	t.manager.BroadcastTeamEvent(ctx, protocol.EventTeamTaskCancelled, BuildTaskEventPayload(
		team.ID.String(), taskID.String(),
		store.TeamTaskStatusCancelled,
		"agent", t.manager.AgentKeyFromID(ctx, agentID),
		WithReason(reason),
		WithContextInfo(ctx),
	))

	// Dependent tasks are dispatched by the consumer after this agent's turn ends (post-turn).

	return NewResult(fmt.Sprintf("Task %s cancelled. Dependent tasks will be unblocked after this turn ends.", taskID))
}

func (t *TeamTasksTool) executeReview(ctx context.Context, args map[string]any) *Result {
	team, agentID, taskID, err := t.resolveTeamAndTask(ctx, args)
	if err != nil {
		return ErrorResult(err.Error())
	}

	// Verify the agent owns this task.
	task, err := t.manager.Store().GetTask(ctx, taskID)
	if err != nil {
		return ErrorResult("task not found: " + err.Error())
	}
	if task.TeamID != team.ID {
		return ErrorResult("task does not belong to your team")
	}
	if task.OwnerAgentID == nil || *task.OwnerAgentID != agentID {
		return ErrorResult("only the task owner can submit for review")
	}
	// review chuyển thẳng sang in_review không marker, không báo cáo tiêu chí:
	// task có contract (team đã bật) hoặc cần user duyệt kết quả phải đi qua
	// complete để vào đúng cổng nghiệm thu và cổng duyệt của user.
	if msg := reviewGateRejection(team, task); msg != "" {
		return ErrorResult(msg)
	}

	if err := t.manager.Store().ReviewTask(ctx, taskID, team.ID); err != nil {
		return ErrorResult("failed to submit for review: " + err.Error())
	}
	// Record action flag after successful store operation.
	recordTaskAction(ctx, func(f *TaskActionFlags) { f.Reviewed = true })

	ownerKey := t.manager.AgentKeyFromID(ctx, agentID)
	t.manager.BroadcastTeamEvent(ctx, protocol.EventTeamTaskReviewed, BuildTaskEventPayload(
		team.ID.String(), taskID.String(),
		store.TeamTaskStatusInReview,
		"agent", ownerKey,
		WithOwner(ownerKey, t.manager.AgentDisplayName(ctx, ownerKey)),
		WithContextInfo(ctx),
	))

	return NewResult(fmt.Sprintf("Task %s submitted for review.", taskID))
}

func (t *TeamTasksTool) executeApprove(ctx context.Context, args map[string]any) *Result {
	// Note: reviewer role not yet active. All approvals flow through leader or dashboard.

	team, agentID, taskID, err := t.resolveTeamAndTask(ctx, args)
	if err != nil {
		return ErrorResult(err.Error())
	}

	// Fetch task for subject (used in lead message) and team ownership check
	task, err := t.manager.Store().GetTask(ctx, taskID)
	if err != nil {
		return ErrorResult("task not found: " + err.Error())
	}
	if task.TeamID != team.ID {
		return ErrorResult("task does not belong to your team")
	}
	// Lớp chặn: task chờ user duyệt thì không agent nào duyệt thay được, bất
	// kể agent nào gọi và lượt chạy ở kênh nào.
	if TaskCheckpointPending(task) == CheckpointPendingUser {
		return ErrorResult(userCheckpointRejection(task))
	}

	// Only lead can approve tasks via tool. Không bỏ qua theo tên kênh: task tạo
	// từ dashboard chạy ở kênh "dashboard", nên member sẽ tự duyệt được. Người
	// duyệt trên Web UI đi qua RPC teams.tasks.approve, không qua tool này.
	if err := t.manager.RequireLead(ctx, team, agentID); err != nil {
		return ErrorResult(err.Error())
	}

	// Task cần user duyệt kết quả mà chưa tới lượt user (chờ lead, hoặc vào
	// in_review không marker qua đường cũ): chuyển tiếp cho user, không hoàn thành.
	if TaskCheckpoint(task) == TaskContractCheckpointResult {
		return t.forwardLeadReviewToUser(ctx, team, task, agentID)
	}

	// Atomic transition: in_review -> completed
	if err := t.manager.Store().ApproveTask(ctx, taskID, team.ID, ""); err != nil {
		return ErrorResult("failed to approve task: " + err.Error())
	}
	// Task chờ lead duyệt: xóa checkpoint_pending sau khi đã completed.
	if TaskCheckpointPending(task) != "" {
		clearCheckpointPending(task)
		_ = t.manager.Store().UpdateTask(ctx, taskID, map[string]any{"metadata": task.Metadata})
	}

	// Re-fetch to get the actual post-approval status (pending or blocked)
	approved, _ := t.manager.Store().GetTask(ctx, taskID)
	newStatus := store.TeamTaskStatusPending
	if approved != nil {
		newStatus = approved.Status
	}

	t.manager.BroadcastTeamEvent(ctx, protocol.EventTeamTaskApproved, BuildTaskEventPayload(
		team.ID.String(), taskID.String(),
		newStatus,
		"agent", t.manager.AgentKeyFromID(ctx, agentID),
		WithSubject(task.Subject),
		WithContextInfo(ctx),
	))

	// Record approval as a task comment for audit trail.
	approveMsg := fmt.Sprintf("Task approved (status: %s).", newStatus)
	_ = t.manager.Store().AddTaskComment(ctx, &store.TeamTaskCommentData{
		TaskID:  taskID,
		AgentID: &agentID,
		Content: approveMsg,
	})

	return NewResult(fmt.Sprintf("Task %s approved (status: %s).", taskID, newStatus))
}

func (t *TeamTasksTool) executeReject(ctx context.Context, args map[string]any) *Result {
	// Note: reviewer role not yet active. Rejections flow through leader or dashboard.

	team, agentID, taskID, err := t.resolveTeamAndTask(ctx, args)
	if err != nil {
		return ErrorResult(err.Error())
	}

	reason, _ := args["text"].(string)
	if reason == "" {
		reason = "Rejected by user"
	}

	// Fetch task to get subject for the lead message
	task, err := t.manager.Store().GetTask(ctx, taskID)
	if err != nil {
		return ErrorResult("task not found: " + err.Error())
	}
	if task.TeamID != team.ID {
		return ErrorResult("task does not belong to your team")
	}
	// Lớp chặn như approve: task chờ user duyệt thì agent không reject được.
	if TaskCheckpointPending(task) == CheckpointPendingUser {
		return ErrorResult(userCheckpointRejection(task))
	}

	// Only lead can reject tasks via tool (cùng lý do như approve: không bỏ qua
	// theo tên kênh; Web UI dùng RPC teams.tasks.reject).
	if err := t.manager.RequireLead(ctx, team, agentID); err != nil {
		return ErrorResult(err.Error())
	}

	// Record rejection as a task comment for audit trail (before status change).
	rejectMsg := fmt.Sprintf("Task rejected. Reason: %s", reason)
	_ = t.manager.Store().AddTaskComment(ctx, &store.TeamTaskCommentData{
		TaskID:  taskID,
		AgentID: &agentID,
		Content: rejectMsg,
	})

	// Auto re-dispatch if task has an owner: skip RejectTask (which unblocks dependents)
	// and instead reset in_review → pending → in_progress → dispatch.
	// Dependents stay blocked until this task actually completes or circuit breaker fails it.
	if task.OwnerAgentID != nil {
		// Lý do reject thành amendment changes_requested của contract, để member
		// thấy yêu cầu sửa trong khối contract của lượt chạy sau.
		if c := TaskContractFromTask(task); c != nil {
			if err := validateFeedbackText(reason); err != nil {
				return ErrorResult("reject: " + err.Error())
			}
			if err := appendTaskAmendment(task, c, AmendmentKindChangesRequested, reason, t.manager.AgentKeyFromID(ctx, agentID)); err != nil {
				return ErrorResult(err.Error())
			}
		}
		clearCheckpointPending(task)
		if task.Metadata != nil {
			if err := t.manager.Store().UpdateTask(ctx, taskID, map[string]any{"metadata": task.Metadata}); err != nil {
				return ErrorResult("failed to save task metadata: " + err.Error())
			}
		}
		// Reset in_review → pending (ResetTaskStatus accepts in_review via store guard).
		if err := t.manager.Store().ResetTaskStatus(ctx, taskID, team.ID); err != nil {
			// Fallback: use RejectTask (cancels + unblocks) if reset fails.
			slog.Warn("reject: reset failed, falling back to RejectTask", "task_id", taskID, "error", err)
			if err := t.manager.Store().RejectTask(ctx, taskID, team.ID, reason); err != nil {
				return ErrorResult("failed to reject task: " + err.Error())
			}
			t.manager.BroadcastTeamEvent(ctx, protocol.EventTeamTaskRejected, BuildTaskEventPayload(
				team.ID.String(), taskID.String(),
				store.TeamTaskStatusCancelled,
				"agent", t.manager.AgentKeyFromID(ctx, agentID),
				WithSubject(task.Subject),
				WithReason(reason),
				WithContextInfo(ctx),
			))
			return NewResult(fmt.Sprintf("Task %s rejected (cancelled). Use retry to re-dispatch manually.", taskID))
		}
		if err := t.manager.Store().AssignTask(ctx, taskID, *task.OwnerAgentID, team.ID); err != nil {
			slog.Warn("reject: assign task failed", "task_id", taskID, "error", err)
			return NewResult(fmt.Sprintf("Task %s rejected but could not assign. Use retry to re-dispatch manually.", taskID))
		}
		t.manager.BroadcastTeamEvent(ctx, protocol.EventTeamTaskRejected, BuildTaskEventPayload(
			team.ID.String(), taskID.String(),
			store.TeamTaskStatusInProgress,
			"agent", t.manager.AgentKeyFromID(ctx, agentID),
			WithSubject(task.Subject),
			WithReason(reason),
			WithContextInfo(ctx),
		))
		// Dispatch có chủ ý: đặt lại bộ đếm để circuit breaker chỉ tính lần lỗi.
		resetDispatchCount(task)
		t.manager.DispatchTaskToAgent(ctx, task, team, *task.OwnerAgentID)
		return NewResult(fmt.Sprintf("Task %s rejected and re-dispatched to %s with feedback.",
			taskID, t.manager.AgentKeyFromID(ctx, *task.OwnerAgentID)))
	}

	// No owner — use RejectTask to cancel + unblock dependents.
	if err := t.manager.Store().RejectTask(ctx, taskID, team.ID, reason); err != nil {
		return ErrorResult("failed to reject task: " + err.Error())
	}
	t.manager.BroadcastTeamEvent(ctx, protocol.EventTeamTaskRejected, BuildTaskEventPayload(
		team.ID.String(), taskID.String(),
		store.TeamTaskStatusCancelled,
		"agent", t.manager.AgentKeyFromID(ctx, agentID),
		WithSubject(task.Subject),
		WithReason(reason),
		WithContextInfo(ctx),
	))
	return NewResult(fmt.Sprintf("Task %s rejected.", taskID))
}
