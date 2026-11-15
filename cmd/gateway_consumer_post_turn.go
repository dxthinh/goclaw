package cmd

import (
	"context"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/scheduler"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// taskLockRenewalInterval là nhịp gia hạn khóa task khi member đang chạy.
// Là biến để test rút ngắn được.
var taskLockRenewalInterval = 5 * time.Minute

// startTaskLockRenewal extends the task lock every 5 min while the agent works.
// Returns a stop func — call it when the run finishes. Returns nil if taskID is
// invalid or teamStore is nil.
func startTaskLockRenewal(ctx context.Context, teamStore store.TeamStore, taskID, teamID uuid.UUID) (stop func()) {
	if taskID == uuid.Nil || teamStore == nil {
		return nil
	}
	ch := make(chan struct{})
	go func() {
		ticker := time.NewTicker(taskLockRenewalInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				// Lỗi một lần (DB chập chờn) không dừng vòng gia hạn: thử lại ở nhịp sau,
				// nếu không khóa hết hạn khi member vẫn đang chạy và ticker recover nhầm.
				if err := teamStore.RenewTaskLock(ctx, taskID, teamID); err != nil {
					slog.Warn("teammate lock renewal failed, will retry next tick", "task_id", taskID, "error", err)
					continue
				}
				slog.Debug("teammate lock renewed", "task_id", taskID)
			case <-ch:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() { close(ch) }
}

// teammateTaskMeta holds resolved task context for post-turn processing.
type teammateTaskMeta struct {
	TaskID     uuid.UUID
	TeamID     uuid.UUID
	ToAgent    string
	Channel    string
	ChatID     string
	PeerKind   string // "group" or "direct" — for correct notification routing (#266)
	Subject    string
	TaskNumber int
}

// resolveTeamTaskOutcome determines the correct task lifecycle action
// after a teammate run: error→fail, completed/escalated→skip, reviewed→renew,
// loopKilled→fail, default→auto-complete.
// Returns the resolved team (for announce routing) and dispatches unblocked tasks.
// failNote is non-empty when this call failed the task and other tasks stay
// blocked by it; the caller appends it to the announce for the lead.
// leadNotice khác rỗng khi task có contract đang chờ lead xem (in_review,
// checkpoint_pending = "lead"); người gọi đặt nó ở đầu tin báo lead.
func resolveTeamTaskOutcome(
	ctx context.Context,
	deps *ConsumerDeps,
	outcome scheduler.RunOutcome,
	flags *tools.TaskActionFlags,
	meta teammateTaskMeta,
) (cachedTeam *store.TeamData, failNote, leadNotice string) {
	if meta.TaskID == uuid.Nil || deps.TeamStore == nil {
		return nil, "", ""
	}

	cachedTeam, _ = deps.TeamStore.GetTeam(ctx, meta.TeamID)
	if cachedTeam == nil {
		return nil, "", ""
	}

	// Check current task status — agent may have already updated it via tool.
	currentTask, taskErr := deps.TeamStore.GetTask(ctx, meta.TaskID)
	alreadyTerminal := taskErr == nil && currentTask != nil &&
		(currentTask.Status == store.TeamTaskStatusCompleted ||
			currentTask.Status == store.TeamTaskStatusFailed ||
			currentTask.Status == store.TeamTaskStatusCancelled)

	if alreadyTerminal {
		// Always dispatch unblocked tasks even if already terminal.
		if deps.PostTurn != nil {
			deps.PostTurn.DispatchUnblockedTasks(ctx, meta.TeamID)
		}
		return cachedTeam, "", ""
	}

	toAgent := meta.ToAgent
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	taskSubject := meta.Subject
	taskNumber := meta.TaskNumber
	taskChannel := meta.Channel
	taskChatID := meta.ChatID
	taskPeerKind := meta.PeerKind
	var taskLocalKey string

	// Enrich with live task data if available.
	if currentTask != nil {
		if currentTask.Subject != "" {
			taskSubject = currentTask.Subject
		}
		if currentTask.TaskNumber != 0 {
			taskNumber = currentTask.TaskNumber
		}
		if currentTask.Channel != "" {
			taskChannel = currentTask.Channel
		}
		if currentTask.ChatID != "" {
			taskChatID = currentTask.ChatID
		}
		if currentTask.Metadata != nil {
			if taskPeerKind == "" {
				if pk, ok := currentTask.Metadata[tools.TaskMetaPeerKind].(string); ok && pk != "" {
					taskPeerKind = pk
				}
			}
			// local_key is required for forum topic routing (e.g. Telegram supergroup threads).
			if lk, ok := currentTask.Metadata[tools.TaskMetaLocalKey].(string); ok {
				taskLocalKey = lk
			}
		}
	}

	// Smart post-turn decision based on action flags.
	// Only error, completed/escalated, and reviewed block auto-complete.
	runFailure := outcome.Failure()
	switch {
	case runFailure != nil:
		// Agent errored or the pipeline stopped the run → auto-fail.
		if err := deps.TeamStore.FailTask(ctx, meta.TaskID, meta.TeamID, runFailure.Error()); err != nil {
			slog.Warn("auto-complete: FailTask error", "task_id", meta.TaskID, "error", err)
		} else {
			failNote = tools.BlockedDependentsNote(ctx, deps.TeamStore, meta.TeamID, meta.TaskID)
			bus.BroadcastForTenant(deps.MsgBus, protocol.EventTeamTaskFailed, store.TenantIDFromContext(ctx), tools.BuildTaskEventPayload(
				meta.TeamID.String(), meta.TaskID.String(),
				store.TeamTaskStatusFailed,
				"agent", toAgent,
				tools.WithTaskInfo(taskNumber, taskSubject),
				tools.WithReason(runFailure.Error()),
				tools.WithChannel(taskChannel),
				tools.WithChatID(taskChatID),
				tools.WithPeerKind(taskPeerKind),
				tools.WithLocalKey(taskLocalKey),
				tools.WithTimestamp(now),
			))
		}

	case flags.Completed || flags.Escalated || flags.AwaitingInput:
		// Tool already completed/failed the task, or the member asked for
		// clarification (task is awaiting_input) — skip auto-complete.
		slog.Info("post-turn: tool handled task", "task_id", meta.TaskID,
			"completed", flags.Completed, "escalated", flags.Escalated, "awaiting_input", flags.AwaitingInput)
		// complete có tiêu chí thiếu/chưa đạt đã đưa task về chờ lead xem.
		if flags.Completed && currentTask != nil && currentTask.Status == store.TeamTaskStatusInReview &&
			tools.TaskCheckpointPending(currentTask) == tools.CheckpointPendingLead {
			leadNotice = tools.LeadReviewNotice(currentTask, false)
		}

	case flags.Reviewed:
		// Task submitted for review — skip auto-complete, renew lock.
		_ = deps.TeamStore.RenewTaskLock(ctx, meta.TaskID, meta.TeamID)
		slog.Info("post-turn: task submitted for review", "task_id", meta.TaskID)

	case outcome.Result != nil && outcome.Result.LoopKilled:
		// Loop detector killed the run → auto-fail instead of auto-complete.
		// The agent was terminated (not stuck in_progress) — mark as failed
		// so the leader sees clear failure signal and can retry directly.
		failMsg := outcome.Result.Content
		if failMsg == "" {
			failMsg = "Agent run terminated by loop detector"
		}
		if err := deps.TeamStore.FailTask(ctx, meta.TaskID, meta.TeamID, failMsg); err != nil {
			slog.Warn("auto-fail: FailTask error (loop kill)", "task_id", meta.TaskID, "error", err)
		} else {
			failNote = tools.BlockedDependentsNote(ctx, deps.TeamStore, meta.TeamID, meta.TaskID)
			bus.BroadcastForTenant(deps.MsgBus, protocol.EventTeamTaskFailed, store.TenantIDFromContext(ctx), tools.BuildTaskEventPayload(
				meta.TeamID.String(), meta.TaskID.String(),
				store.TeamTaskStatusFailed,
				"system", "loop_detector",
				tools.WithTaskInfo(taskNumber, taskSubject),
				tools.WithReason("loop_detector_kill"),
				tools.WithChannel(taskChannel),
				tools.WithChatID(taskChatID),
				tools.WithPeerKind(taskPeerKind),
				tools.WithLocalKey(taskLocalKey),
				tools.WithTimestamp(now),
			))
		}
		slog.Warn("post-turn: loop detector killed member run",
			"task_id", meta.TaskID, "agent", toAgent)

	default:
		// Agent turn ended without terminal action — auto-complete.
		// Covers: Progressed, Commented, Claimed, or no flags at all.
		result := ""
		if outcome.Result != nil {
			result = outcome.Result.Content
			if len(outcome.Result.Deliverables) > 0 {
				result = strings.Join(outcome.Result.Deliverables, "\n\n---\n\n")
			}
		}
		if result == "" {
			result = "Agent run ended without explicit result"
		}
		result = truncateTaskResult(result)
		// Task có contract (team đã bật) mà lượt chạy không gọi complete: không
		// tự hoàn thành, chuyển sang chờ lead xem.
		if contract := tools.GatedTaskContract(cachedTeam, currentTask); contract != nil {
			if err := tools.SubmitResultForLeadReview(ctx, deps.TeamStore, cachedTeam, currentTask, result, nil); err != nil {
				slog.Warn("post-turn: submit for lead review failed", "task_id", meta.TaskID, "error", err)
				break
			}
			leadNotice = tools.LeadReviewNotice(currentTask, true)
			bus.BroadcastForTenant(deps.MsgBus, protocol.EventTeamTaskReviewed, store.TenantIDFromContext(ctx), tools.BuildTaskEventPayload(
				meta.TeamID.String(), meta.TaskID.String(),
				store.TeamTaskStatusInReview,
				"agent", toAgent,
				tools.WithTaskInfo(taskNumber, taskSubject),
				tools.WithOwnerAgentKey(toAgent),
				tools.WithChannel(taskChannel),
				tools.WithChatID(taskChatID),
				tools.WithPeerKind(taskPeerKind),
				tools.WithLocalKey(taskLocalKey),
				tools.WithTimestamp(now),
			))
			break
		}
		// Cổng duyệt dùng chung với tool complete: task có checkpoint "result"
		// được nộp chờ user duyệt thay vì completed.
		if deps.TaskFeedback != nil && currentTask != nil {
			submitted, err := deps.TaskFeedback.SubmitResultForUserApproval(ctx, cachedTeam, currentTask, result, tools.TaskActor{AgentKey: toAgent})
			if submitted {
				if err != nil {
					slog.Warn("post-turn: submit for user approval failed", "task_id", meta.TaskID, "error", err)
				}
				break
			}
		}
		if err := deps.TeamStore.CompleteTask(ctx, meta.TaskID, meta.TeamID, result); err != nil {
			slog.Warn("auto-complete: CompleteTask error", "task_id", meta.TaskID, "error", err)
		} else {
			bus.BroadcastForTenant(deps.MsgBus, protocol.EventTeamTaskCompleted, store.TenantIDFromContext(ctx), tools.BuildTaskEventPayload(
				meta.TeamID.String(), meta.TaskID.String(),
				store.TeamTaskStatusCompleted,
				"agent", toAgent,
				tools.WithTaskInfo(taskNumber, taskSubject),
				tools.WithOwnerAgentKey(toAgent),
				tools.WithChannel(taskChannel),
				tools.WithChatID(taskChatID),
				tools.WithPeerKind(taskPeerKind),
				tools.WithLocalKey(taskLocalKey),
				tools.WithTimestamp(now),
			))
		}
	}

	// Always dispatch unblocked tasks after member turn ends,
	// regardless of whether the task was already completed by the tool.
	// This ensures dependent tasks start only after the member's run finishes.
	if deps.PostTurn != nil {
		deps.PostTurn.DispatchUnblockedTasks(ctx, meta.TeamID)
	}

	return cachedTeam, failNote, leadNotice
}

// taskResultMaxRunes là độ dài tối đa (ký tự) của result tự ghi sau lượt chạy.
const taskResultMaxRunes = 100_000

// truncateTaskResult cắt result theo ký tự (rune), không theo byte, để không
// cắt đôi ký tự nhiều byte (tiếng Việt, CJK) và result luôn là UTF-8 hợp lệ.
func truncateTaskResult(result string) string {
	if utf8.RuneCountInString(result) <= taskResultMaxRunes {
		return result
	}
	return string([]rune(result)[:taskResultMaxRunes]) + "\n[truncated]"
}
