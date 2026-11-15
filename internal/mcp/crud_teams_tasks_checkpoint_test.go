package mcp

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// reviewTaskTeamStore ghi lại các lần ApproveTask/RejectTask để kiểm tra MCP
// có chạm store hay không.
type reviewTaskTeamStore struct {
	*contractTaskTeamStore
	approved []uuid.UUID
	rejected []uuid.UUID
}

func (s *reviewTaskTeamStore) ApproveTask(_ context.Context, taskID, _ uuid.UUID, _ string) error {
	s.approved = append(s.approved, taskID)
	return nil
}

func (s *reviewTaskTeamStore) RejectTask(_ context.Context, taskID, _ uuid.UUID, _ string) error {
	s.rejected = append(s.rejected, taskID)
	return nil
}

func (s *reviewTaskTeamStore) AddTaskComment(context.Context, *store.TeamTaskCommentData) error {
	return nil
}

// Task chờ user duyệt không được approve/reject qua MCP; task chờ lead hoặc
// in_review thường thì vẫn đi qua như trước.
func TestMCPTasksApproveReject_UserCheckpointDenied(t *testing.T) {
	ts := &reviewTaskTeamStore{contractTaskTeamStore: &contractTaskTeamStore{fakeTeamStore: newFakeTeamStore(), tasks: map[uuid.UUID]*store.TeamTaskData{}}}
	team := &store.TeamData{Name: "t"}
	require.NoError(t, ts.CreateTeam(context.Background(), team))
	srv := newTestMCPServer()
	registerTeamsTasksCRUDTools(srv, ts, newFakeAgentStore(), nil)

	newTask := func(pending string) uuid.UUID {
		meta := map[string]any{}
		if pending != "" {
			meta[tools.TaskMetaCheckpointPending] = pending
		}
		task := &store.TeamTaskData{TeamID: team.ID, Status: store.TeamTaskStatusInReview, Metadata: meta}
		require.NoError(t, ts.CreateTask(context.Background(), task))
		return task.ID
	}
	call := func(name string, taskID uuid.UUID) (bool, string) {
		res := callTool(t, srv, name, map[string]any{"team_id": team.ID.String(), "task_id": taskID.String()})
		return toolIsError(res), toolResultText(res)
	}

	userTask := newTask(tools.CheckpointPendingUser)
	for _, name := range []string{"goclaw_teams_tasks_approve", "goclaw_teams_tasks_reject"} {
		isErr, text := call(name, userTask)
		assert.True(t, isErr, "%s on user checkpoint must fail: %s", name, text)
		assert.Contains(t, text, "waiting for the user's approval")
	}
	assert.Empty(t, ts.approved)
	assert.Empty(t, ts.rejected)

	leadTask, plainTask := newTask(tools.CheckpointPendingLead), newTask("")
	isErr, text := call("goclaw_teams_tasks_approve", leadTask)
	require.False(t, isErr, text)
	isErr, text = call("goclaw_teams_tasks_reject", plainTask)
	require.False(t, isErr, text)
	assert.Equal(t, []uuid.UUID{leadTask}, ts.approved)
	assert.Equal(t, []uuid.UUID{plainTask}, ts.rejected)

	// Task chờ lead mà contract đòi user duyệt kết quả: approve qua MCP bị chặn
	// (nếu không sẽ hoàn thành task, bỏ qua user); reject vẫn được.
	needsUser := &store.TeamTaskData{TeamID: team.ID, Status: store.TeamTaskStatusInReview, Metadata: map[string]any{
		tools.TaskMetaCheckpointPending: tools.CheckpointPendingLead,
		tools.TaskMetaContract: map[string]any{"objective": "o", "deliverables": []any{"d"}, "acceptance": []any{"a"},
			"checkpoint": tools.TaskContractCheckpointResult},
	}}
	require.NoError(t, ts.CreateTask(context.Background(), needsUser))
	isErr, text = call("goclaw_teams_tasks_approve", needsUser.ID)
	assert.True(t, isErr, "approve of a lead-review task that needs the user must fail: %s", text)
	assert.Contains(t, text, "needs the user's approval")
	assert.Equal(t, []uuid.UUID{leadTask}, ts.approved)
	isErr, text = call("goclaw_teams_tasks_reject", needsUser.ID)
	require.False(t, isErr, text)

	// Task cần user duyệt nằm ở in_review không marker (member gọi review ở
	// đường cũ): approve qua MCP cũng bị chặn.
	noMarker := &store.TeamTaskData{TeamID: team.ID, Status: store.TeamTaskStatusInReview, Metadata: map[string]any{
		tools.TaskMetaCheckpoint: tools.TaskContractCheckpointResult,
	}}
	require.NoError(t, ts.CreateTask(context.Background(), noMarker))
	isErr, text = call("goclaw_teams_tasks_approve", noMarker.ID)
	assert.True(t, isErr, "approve of a reviewed task that needs the user must fail: %s", text)
	assert.Contains(t, text, "needs the user's approval")
	assert.Equal(t, []uuid.UUID{leadTask}, ts.approved)

	// Task không thuộc team: lỗi, không chạm store.
	other := &store.TeamTaskData{TeamID: uuid.New(), Status: store.TeamTaskStatusInReview}
	require.NoError(t, ts.CreateTask(context.Background(), other))
	isErr, _ = call("goclaw_teams_tasks_approve", other.ID)
	assert.True(t, isErr)
	assert.Len(t, ts.approved, 1)
}
