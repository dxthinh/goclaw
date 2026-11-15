package methods

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// rpcFeedback ghi lại lời gọi tới dịch vụ feedback dùng chung.
type rpcFeedback struct {
	answers  []string
	changes  []string
	approved int
	actor    tools.TaskActor
	err      error
}

func (f *rpcFeedback) AnswerTask(_ context.Context, _ *store.TeamData, _ *store.TeamTaskData, text string, _ uuid.UUID, a tools.TaskActor) error {
	f.answers, f.actor = append(f.answers, text), a
	return f.err
}
func (f *rpcFeedback) RequestTaskChanges(_ context.Context, _ *store.TeamData, _ *store.TeamTaskData, reason string, a tools.TaskActor) error {
	f.changes, f.actor = append(f.changes, reason), a
	return f.err
}
func (f *rpcFeedback) ApproveUserCheckpoint(_ context.Context, _ *store.TeamData, _ *store.TeamTaskData, a tools.TaskActor) error {
	f.approved++
	f.actor = a
	return f.err
}
func (f *rpcFeedback) ChannelTaskApprovalEnabled(string) bool { return true }
func (f *rpcFeedback) SubmitResultForUserApproval(context.Context, *store.TeamData, *store.TeamTaskData, string, tools.TaskActor) (bool, error) {
	return false, nil
}

func TestTaskAnswerRPC_CallsSharedFeedback(t *testing.T) {
	f := newHumanTaskFixture(t, store.TeamTaskStatusAwaitingInput)
	fb := &rpcFeedback{}
	f.m.SetTaskFeedback(fb)
	ok, code, msg := f.call(t, f.m.handleTaskAnswer, map[string]any{
		"teamId": f.teamID.String(), "taskId": f.taskID.String(), "text": " Q3 2026 ",
	})
	if !ok {
		t.Fatalf("expected ok, got %s: %s", code, msg)
	}
	if len(fb.answers) != 1 || fb.answers[0] != "Q3 2026" || fb.actor.UserID != "human-1" || fb.actor.AgentID != nil {
		t.Fatalf("answers = %v actor = %+v", fb.answers, fb.actor)
	}
}

func TestTaskAnswerRPC_ErrorsAreTranslated(t *testing.T) {
	f := newHumanTaskFixture(t, store.TeamTaskStatusInProgress)
	f.m.SetTaskFeedback(&rpcFeedback{err: tools.ErrTaskNotAwaitingInput})
	ok, code, _ := f.call(t, f.m.handleTaskAnswer, map[string]any{
		"teamId": f.teamID.String(), "taskId": f.taskID.String(), "text": "x",
	})
	if ok || code != protocol.ErrInvalidRequest {
		t.Fatalf("expected INVALID_REQUEST, got ok=%v code=%s", ok, code)
	}
}

func TestTaskFeedbackRPC_WrongTeamIsNotFound(t *testing.T) {
	f := newHumanTaskFixture(t, store.TeamTaskStatusInReview)
	fb := &rpcFeedback{}
	f.m.SetTaskFeedback(fb)
	ok, code, _ := f.call(t, f.m.handleTaskRequestChanges, map[string]any{
		"teamId": uuid.NewString(), "taskId": f.taskID.String(), "reason": "redo",
	})
	if ok || code != protocol.ErrNotFound || len(fb.changes) != 0 {
		t.Fatalf("expected NOT_FOUND without changes, got ok=%v code=%s changes=%v", ok, code, fb.changes)
	}
}

func TestTaskRequestChangesRPC_CallsSharedFeedback(t *testing.T) {
	f := newHumanTaskFixture(t, store.TeamTaskStatusInReview)
	fb := &rpcFeedback{}
	f.m.SetTaskFeedback(fb)
	if ok, code, msg := f.call(t, f.m.handleTaskRequestChanges, map[string]any{
		"teamId": f.teamID.String(), "taskId": f.taskID.String(), "reason": "add sources",
	}); !ok {
		t.Fatalf("expected ok, got %s: %s", code, msg)
	}
	if len(fb.changes) != 1 || fb.changes[0] != "add sources" {
		t.Fatalf("changes = %v", fb.changes)
	}
}

// teams.tasks.approve trên task chờ user duyệt đi qua ApproveUserCheckpoint.
func TestTaskApproveRPC_UserCheckpointUsesSharedPath(t *testing.T) {
	f := newHumanTaskFixture(t, store.TeamTaskStatusInReview)
	f.st.task.Metadata = map[string]any{tools.TaskMetaCheckpointPending: tools.CheckpointPendingUser}
	fb := &rpcFeedback{}
	f.m.SetTaskFeedback(fb)
	if ok, code, msg := f.call(t, f.m.handleTaskApprove, map[string]any{
		"teamId": f.teamID.String(), "taskId": f.taskID.String(),
	}); !ok {
		t.Fatalf("expected ok, got %s: %s", code, msg)
	}
	if fb.approved != 1 {
		t.Fatalf("ApproveUserCheckpoint calls = %d, want 1", fb.approved)
	}
}
