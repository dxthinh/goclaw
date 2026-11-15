package methods

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/gateway"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// SetTaskFeedback nối dịch vụ feedback (trả lời clarify, yêu cầu sửa, duyệt
// checkpoint của user) dùng chung với tool team_tasks và lệnh chat.
func (m *TeamsMethods) SetTaskFeedback(fb tools.TaskFeedbackService) {
	m.taskFeedback = fb
}

// loadFeedbackTask đọc team và task cho các RPC feedback, kiểm tra task thuộc
// team (chống IDOR). Trả false khi đã gửi lỗi cho client.
func (m *TeamsMethods) loadFeedbackTask(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame, locale, rawTeamID, rawTaskID string) (*store.TeamData, *store.TeamTaskData, bool) {
	fail := func(code, msg string) (*store.TeamData, *store.TeamTaskData, bool) {
		client.SendResponse(protocol.NewErrorResponse(req.ID, code, msg))
		return nil, nil, false
	}
	teamID, err := uuid.Parse(rawTeamID)
	if err != nil {
		return fail(protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidID, "teamId"))
	}
	taskID, err := uuid.Parse(rawTaskID)
	if err != nil {
		return fail(protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidID, "taskId"))
	}
	if m.taskFeedback == nil {
		return fail(protocol.ErrInternal, i18n.T(locale, i18n.MsgTeamsNotConfigured))
	}
	task, err := m.teamStore.GetTask(ctx, taskID)
	if err != nil || task == nil || task.TeamID != teamID {
		if err != nil && !errors.Is(err, store.ErrTaskNotFound) {
			slog.Warn("teams.tasks.feedback get task failed", "task_id", taskID, "error", err)
			return fail(protocol.ErrInternal, i18n.T(locale, i18n.MsgInternalError, ""))
		}
		return fail(protocol.ErrNotFound, i18n.T(locale, i18n.MsgNotFound, "task", ""))
	}
	team, err := m.teamStore.GetTeam(ctx, teamID)
	if err != nil || team == nil {
		slog.Warn("teams.tasks.feedback get team failed", "team_id", teamID, "error", err)
		return fail(protocol.ErrInternal, i18n.T(locale, i18n.MsgInternalError, ""))
	}
	return team, task, true
}

// feedbackErrorMessage dịch lỗi của thao tác feedback cho client.
func feedbackErrorMessage(locale string, err error) string {
	switch {
	case errors.Is(err, tools.ErrTaskNotAwaitingInput):
		return i18n.T(locale, i18n.MsgTaskNotAwaitingInput)
	case errors.Is(err, tools.ErrTaskNotInReview):
		return i18n.T(locale, i18n.MsgTaskNotInReview)
	case errors.Is(err, tools.ErrTaskContractMissing):
		return i18n.T(locale, i18n.MsgTaskContractRequired)
	default:
		return i18n.T(locale, i18n.MsgTaskFeedbackInvalid, err.Error())
	}
}

// --- Task Answer (trả lời câu hỏi clarify của member) ---

type taskAnswerParams struct {
	TeamID   string `json:"teamId"`
	TaskID   string `json:"taskId"`
	Text     string `json:"text"`
	Assignee string `json:"assignee"`
}

func (m *TeamsMethods) handleTaskAnswer(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params taskAnswerParams
	locale, ok := m.parseTaskParams(ctx, client, req, &params)
	if !ok {
		return
	}
	team, task, ok := m.loadFeedbackTask(ctx, client, req, locale, params.TeamID, params.TaskID)
	if !ok {
		return
	}
	assignee := uuid.Nil
	if key := strings.TrimSpace(params.Assignee); key != "" {
		id, err := resolveAgentUUIDCached(ctx, m.agentRouter, m.agentStore, key)
		if err != nil || !m.isTeamMember(ctx, team.ID, id) {
			client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, i18n.T(locale, i18n.MsgInvalidID, "assignee")))
			return
		}
		assignee = id
	}
	actor := tools.TaskActor{UserID: client.UserID()}
	if err := m.taskFeedback.AnswerTask(ctx, team, task, strings.TrimSpace(params.Text), assignee, actor); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, feedbackErrorMessage(locale, err)))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"ok": true}))
}

// isTeamMember cho biết agentID là member của team.
func (m *TeamsMethods) isTeamMember(ctx context.Context, teamID, agentID uuid.UUID) bool {
	members, err := m.teamStore.ListMembers(ctx, teamID)
	if err != nil {
		return false
	}
	for _, mem := range members {
		if mem.AgentID == agentID {
			return true
		}
	}
	return false
}

// --- Task Request Changes (yêu cầu sửa kết quả, task được làm lại) ---

type taskRequestChangesParams struct {
	TeamID string `json:"teamId"`
	TaskID string `json:"taskId"`
	Reason string `json:"reason"`
}

func (m *TeamsMethods) handleTaskRequestChanges(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame) {
	var params taskRequestChangesParams
	locale, ok := m.parseTaskParams(ctx, client, req, &params)
	if !ok {
		return
	}
	team, task, ok := m.loadFeedbackTask(ctx, client, req, locale, params.TeamID, params.TaskID)
	if !ok {
		return
	}
	actor := tools.TaskActor{UserID: client.UserID()}
	if err := m.taskFeedback.RequestTaskChanges(ctx, team, task, strings.TrimSpace(params.Reason), actor); err != nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, feedbackErrorMessage(locale, err)))
		return
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"ok": true}))
}

// approveUserCheckpoint xử lý teams.tasks.approve cho task đang chờ user
// duyệt: đi qua ApproveUserCheckpoint (completed, dispatch task phụ thuộc,
// báo lead). Trả false khi task không chờ user để handler giữ hành vi cũ.
func (m *TeamsMethods) approveUserCheckpoint(ctx context.Context, client *gateway.Client, req *protocol.RequestFrame, locale string, teamID, taskID uuid.UUID, comment string) bool {
	if m.taskFeedback == nil {
		return false
	}
	task, err := m.teamStore.GetTask(ctx, taskID)
	if err != nil || task == nil || task.TeamID != teamID || tools.TaskCheckpointPending(task) != tools.CheckpointPendingUser {
		return false
	}
	team, err := m.teamStore.GetTeam(ctx, teamID)
	if err != nil || team == nil {
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInternal, i18n.T(locale, i18n.MsgInternalError, "")))
		return true
	}
	if err := m.taskFeedback.ApproveUserCheckpoint(ctx, team, task, tools.TaskActor{UserID: client.UserID()}); err != nil {
		slog.Warn("teams.tasks.approve user checkpoint failed", "task_id", taskID, "error", err)
		client.SendResponse(protocol.NewErrorResponse(req.ID, protocol.ErrInvalidRequest, feedbackErrorMessage(locale, err)))
		return true
	}
	if comment != "" {
		if err := m.teamStore.AddTaskComment(ctx, &store.TeamTaskCommentData{TaskID: taskID, UserID: client.UserID(), Content: comment}); err != nil {
			slog.Warn("audit.comment_failed", "task_id", taskID, "error", err)
		}
	}
	client.SendResponse(protocol.NewOKResponse(req.ID, map[string]any{"ok": true}))
	return true
}

// clearLeadCheckpoint xóa checkpoint_pending ("lead") của task vừa được duyệt
// trên dashboard, để metadata không còn ghi task đang chờ duyệt.
func (m *TeamsMethods) clearLeadCheckpoint(ctx context.Context, taskID uuid.UUID) {
	task, err := m.teamStore.GetTask(ctx, taskID)
	if err != nil || task == nil || tools.TaskCheckpointPending(task) == "" {
		return
	}
	delete(task.Metadata, tools.TaskMetaCheckpointPending)
	if err := m.teamStore.UpdateTask(ctx, taskID, map[string]any{"metadata": task.Metadata}); err != nil {
		slog.Warn("teams.tasks.approve: clear checkpoint_pending failed", "task_id", taskID, "error", err)
	}
}
