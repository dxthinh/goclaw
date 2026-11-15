package tools

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// Loại amendment của contract (contract.md mục 2).
const (
	AmendmentKindAnswer           = "answer"
	AmendmentKindChangesRequested = "changes_requested"
	AmendmentKindReassigned       = "reassigned"
)

// TaskActor là người thực hiện một thao tác feedback: agent (lead) hoặc người
// dùng (dashboard, lệnh chat). Dùng cho audit, comment và trường by của amendment.
type TaskActor struct {
	AgentID  *uuid.UUID // agent thực hiện; nil khi là người dùng
	AgentKey string
	UserID   string // ID người dùng khi AgentID nil
}

func (a TaskActor) actorType() string {
	if a.AgentID != nil || a.AgentKey != "" {
		return "agent"
	}
	return "human"
}

func (a TaskActor) actorID() string {
	if a.AgentKey != "" {
		return a.AgentKey
	}
	return a.UserID
}

// by là giá trị trường by của amendment: agent key của lead, hoặc "user".
func (a TaskActor) by() string {
	if a.AgentKey != "" {
		return a.AgentKey
	}
	return "user"
}

// taskFeedbackBackend là phần của TeamToolBackend mà các thao tác feedback cần.
// Cả *TeamToolManager lẫn mock backend trong test đều thỏa.
type taskFeedbackBackend interface {
	Store() store.TeamStore
	AgentKeyFromID(ctx context.Context, id uuid.UUID) string
	BroadcastTeamEvent(ctx context.Context, name string, payload any)
	DispatchTaskToAgent(ctx context.Context, task *store.TeamTaskData, team *store.TeamData, agentID uuid.UUID)
}

// Lỗi của các thao tác feedback; thông báo bằng tiếng Anh vì trả cho LLM.
var (
	ErrTaskNotAwaitingInput = errors.New("task is not waiting for input (status must be awaiting_input)")
	ErrTaskNotInReview      = errors.New("task is not in review")
	ErrTaskNoOwner          = errors.New("task has no assignee to dispatch to")
	ErrFeedbackTextRequired = errors.New("text is required")
)

// validateFeedbackText kiểm tra nội dung câu trả lời hoặc lý do yêu cầu sửa
// theo giới hạn chuỗi của contract.
func validateFeedbackText(text string) error {
	if text == "" {
		return ErrFeedbackTextRequired
	}
	if utf8.RuneCountInString(text) > taskContractMaxString {
		return fmt.Errorf("text exceeds %d characters", taskContractMaxString)
	}
	return nil
}

// appendTaskAmendment thêm một amendment vào contract của task (sửa trực tiếp
// task.Metadata, chưa ghi DB) và tăng version. Trả lỗi khi vượt giới hạn.
func appendTaskAmendment(task *store.TeamTaskData, c *TaskContract, kind, text, by string) error {
	if c.Version < 1 {
		c.Version = 1
	}
	c.Version++
	c.Amendments = append(c.Amendments, TaskContractAmendment{
		Version: c.Version, Text: text, By: by, At: time.Now().UTC(), Kind: kind,
	})
	if err := c.Validate(); err != nil {
		return fmt.Errorf("cannot amend contract: %w", err)
	}
	if task.Metadata == nil {
		task.Metadata = make(map[string]any)
	}
	task.Metadata[TaskMetaContract] = c.ToMetadata()
	return nil
}

// recordFeedback ghi nội dung phản hồi vào task: thành amendment khi task có
// contract, thành comment khi không. Metadata được ghi ở bước dispatch lại.
func recordFeedback(ctx context.Context, b taskFeedbackBackend, task *store.TeamTaskData, kind, text, commentPrefix string, actor TaskActor) error {
	if c := TaskContractFromTask(task); c != nil {
		return appendTaskAmendment(task, c, kind, text, actor.by())
	}
	return b.Store().AddTaskComment(ctx, &store.TeamTaskCommentData{
		TaskID:  task.ID,
		AgentID: actor.AgentID,
		UserID:  actor.UserID,
		Content: commentPrefix + text,
	})
}

// redispatchTask đưa task về pending, giao cho assignee và dispatch lại có chủ
// ý (đặt lại bộ đếm để circuit breaker chỉ tính lần lỗi). Ghi metadata của
// task (amendment, xóa checkpoint_pending) trước khi dispatch.
func redispatchTask(ctx context.Context, b taskFeedbackBackend, team *store.TeamData, task *store.TeamTaskData, assignee uuid.UUID, event string, actor TaskActor, reason string) error {
	if assignee == team.LeadAgentID {
		return errors.New("cannot dispatch the task to the team lead — assign a team member")
	}
	if err := CheckTaskContractForAssign(team, task); err != nil {
		return err
	}
	if err := checkRedispatchNeeds(ctx, b, team, task, assignee); err != nil {
		return err
	}
	if task.Metadata != nil {
		if err := b.Store().UpdateTask(ctx, task.ID, map[string]any{"metadata": task.Metadata}); err != nil {
			return fmt.Errorf("failed to save task metadata: %w", err)
		}
	}
	if err := b.Store().ResetTaskStatus(ctx, task.ID, team.ID); err != nil {
		return fmt.Errorf("failed to reset task: %w", err)
	}
	if err := b.Store().AssignTask(ctx, task.ID, assignee, team.ID); err != nil {
		return fmt.Errorf("failed to assign task: %w", err)
	}
	task.OwnerAgentID = &assignee
	task.Status = store.TeamTaskStatusInProgress
	assigneeKey := b.AgentKeyFromID(ctx, assignee)
	b.BroadcastTeamEvent(ctx, event, BuildTaskEventPayload(
		team.ID.String(), task.ID.String(),
		store.TeamTaskStatusInProgress,
		actor.actorType(), actor.actorID(),
		WithTaskInfo(task.TaskNumber, task.Subject),
		WithOwnerAgentKey(assigneeKey),
		WithReason(reason),
		WithChannel(task.Channel),
		WithChatID(task.ChatID),
		WithLocalKey(taskLocalKey(task)),
	))
	resetDispatchCount(task)
	b.DispatchTaskToAgent(ctx, task, team, assignee)
	return nil
}

// AnswerTask trả lời câu hỏi clarify của member: lưu câu trả lời (amendment
// "answer" hoặc comment), hủy nhắc định kỳ rồi dispatch lại cho owner hiện tại,
// hoặc cho assignee mới (thêm amendment "reassigned"). assignee = uuid.Nil
// giữ owner hiện tại. Người gọi kiểm tra quyền và assignee là member của team.
func AnswerTask(ctx context.Context, b taskFeedbackBackend, team *store.TeamData, task *store.TeamTaskData, text string, assignee uuid.UUID, actor TaskActor) error {
	if task.Status != store.TeamTaskStatusAwaitingInput {
		return ErrTaskNotAwaitingInput
	}
	if err := validateFeedbackText(text); err != nil {
		return err
	}
	target := assignee
	if target == uuid.Nil {
		if task.OwnerAgentID == nil {
			return ErrTaskNoOwner
		}
		target = *task.OwnerAgentID
	}
	if err := recordFeedback(ctx, b, task, AmendmentKindAnswer, text, "Answer: ", actor); err != nil {
		return err
	}
	if task.OwnerAgentID == nil || *task.OwnerAgentID != target {
		if c := TaskContractFromTask(task); c != nil {
			note := "Task reassigned to " + b.AgentKeyFromID(ctx, target)
			if err := appendTaskAmendment(task, c, AmendmentKindReassigned, note, actor.by()); err != nil {
				return err
			}
		}
	}
	if err := b.Store().ClearTaskFollowup(ctx, task.ID); err != nil {
		return fmt.Errorf("failed to clear follow-up: %w", err)
	}
	return redispatchTask(ctx, b, team, task, target, protocol.EventTeamTaskDispatched, actor, "")
}

// RequestTaskChanges yêu cầu sửa một task đang in_review: lý do thành
// amendment "changes_requested" (hoặc comment khi không có contract), xóa
// checkpoint_pending rồi dispatch lại cho owner.
func RequestTaskChanges(ctx context.Context, b taskFeedbackBackend, team *store.TeamData, task *store.TeamTaskData, reason string, actor TaskActor) error {
	if task.Status != store.TeamTaskStatusInReview {
		return ErrTaskNotInReview
	}
	if err := validateFeedbackText(reason); err != nil {
		return err
	}
	if task.OwnerAgentID == nil {
		return ErrTaskNoOwner
	}
	if err := recordFeedback(ctx, b, task, AmendmentKindChangesRequested, reason, "Changes requested: ", actor); err != nil {
		return err
	}
	clearCheckpointPending(task)
	return redispatchTask(ctx, b, team, task, *task.OwnerAgentID, protocol.EventTeamTaskRejected, actor, reason)
}

// taskLocalKey đọc local_key (forum topic) trong metadata của task.
func taskLocalKey(task *store.TeamTaskData) string {
	if task == nil || task.Metadata == nil {
		return ""
	}
	lk, _ := task.Metadata[TaskMetaLocalKey].(string)
	return lk
}

// checkRedispatchNeeds áp cổng needs của contract (xem CheckAssignee) cho
// assignee của lần dispatch lại, giống assign và retry. Backend không hỗ trợ
// kiểm tra, team chưa bật contract hoặc contract không có needs thì bỏ qua.
// Loại kênh không biết ở đây (lệnh chat, dashboard) nên không lọc theo kênh.
func checkRedispatchNeeds(ctx context.Context, b taskFeedbackBackend, team *store.TeamData, task *store.TeamTaskData, assignee uuid.UUID) error {
	c, ok := b.(teamTaskNeedsChecker)
	if !ok || !ParseTaskContractConfig(team.Settings).Enabled {
		return nil
	}
	contract := TaskContractFromTask(task)
	if contract == nil || len(contract.Needs) == 0 {
		return nil
	}
	members, err := b.Store().ListMembers(ctx, team.ID)
	if err != nil {
		return fmt.Errorf("failed to load team members: %w", err)
	}
	_, err = c.CheckAssignee(ctx, team, members, contract, assignee, "", false)
	return err
}
