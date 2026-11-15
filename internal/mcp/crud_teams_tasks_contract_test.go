package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// contractTaskTeamStore bổ sung các hàm task cần cho create/assign qua MCP.
type contractTaskTeamStore struct {
	*fakeTeamStore
	tasks    map[uuid.UUID]*store.TeamTaskData
	assigned []uuid.UUID
}

func (s *contractTaskTeamStore) CreateTask(_ context.Context, task *store.TeamTaskData) error {
	task.ID = uuid.New()
	cp := *task
	s.tasks[task.ID] = &cp
	return nil
}

func (s *contractTaskTeamStore) GetTask(_ context.Context, id uuid.UUID) (*store.TeamTaskData, error) {
	if t, ok := s.tasks[id]; ok {
		cp := *t
		return &cp, nil
	}
	return nil, store.ErrTaskNotFound
}

func (s *contractTaskTeamStore) AssignTask(_ context.Context, taskID, agentID, _ uuid.UUID) error {
	s.assigned = append(s.assigned, taskID)
	return nil
}

func newContractMCPFixture(t *testing.T, enabled bool) (*contractTaskTeamStore, uuid.UUID, uuid.UUID, func(string, map[string]any) (bool, string)) {
	t.Helper()
	ts := &contractTaskTeamStore{fakeTeamStore: newFakeTeamStore(), tasks: map[uuid.UUID]*store.TeamTaskData{}}
	team := &store.TeamData{Name: "t"}
	if enabled {
		team.Settings = json.RawMessage(`{"task_contract":{"enabled":true}}`)
	}
	require.NoError(t, ts.CreateTeam(context.Background(), team))
	agents := newFakeAgentStore()
	memberID := uuid.New()
	agents.add(&store.AgentData{BaseModel: store.BaseModel{ID: memberID}, AgentKey: "member"})
	srv := newTestMCPServer()
	registerTeamsTasksCRUDTools(srv, ts, agents)
	call := func(name string, args map[string]any) (bool, string) {
		res := callTool(t, srv, name, args)
		return toolIsError(res), toolResultText(res)
	}
	return ts, team.ID, memberID, call
}

func mcpValidContract() map[string]any {
	return map[string]any{"objective": "Brief", "deliverables": []any{"q3.md"}, "acceptance": []any{"All regions"}}
}

func TestMCPTasksCreate_EnabledTeamAssignRequiresContract(t *testing.T) {
	ts, teamID, memberID, call := newContractMCPFixture(t, true)
	isErr, text := call("goclaw_teams_tasks_create", map[string]any{
		"team_id": teamID.String(), "subject": "Q3", "assign_to": memberID.String(),
	})
	assert.True(t, isErr, text)
	assert.Contains(t, text, "task contract")
	assert.Empty(t, ts.tasks)

	isErr, text = call("goclaw_teams_tasks_create", map[string]any{
		"team_id": teamID.String(), "subject": "Q3", "assign_to": memberID.String(), "contract": mcpValidContract(),
	})
	require.False(t, isErr, text)
	require.Len(t, ts.tasks, 1)
	assert.Len(t, ts.assigned, 1)
	for _, task := range ts.tasks {
		c := tools.TaskContractFromTask(task)
		require.NotNil(t, c)
		assert.Equal(t, 1, c.Version)
	}
}

func TestMCPTasksCreate_BacklogAndInvalidContract(t *testing.T) {
	ts, teamID, _, call := newContractMCPFixture(t, true)
	isErr, text := call("goclaw_teams_tasks_create", map[string]any{"team_id": teamID.String(), "subject": "backlog"})
	require.False(t, isErr, text)

	isErr, text = call("goclaw_teams_tasks_create", map[string]any{
		"team_id": teamID.String(), "subject": "bad", "contract": map[string]any{"objective": "x"},
	})
	assert.True(t, isErr)
	assert.Contains(t, text, "deliverables, acceptance")
	assert.Len(t, ts.tasks, 1)
}

func TestMCPTasksAssign_EnabledTeamRequiresContract(t *testing.T) {
	ts, teamID, memberID, call := newContractMCPFixture(t, true)
	taskID := uuid.New()
	ts.tasks[taskID] = &store.TeamTaskData{BaseModel: store.BaseModel{ID: taskID}, TeamID: teamID, TaskType: "general"}
	args := map[string]any{"team_id": teamID.String(), "task_id": taskID.String(), "agent_id": memberID.String()}

	isErr, text := call("goclaw_teams_tasks_assign", args)
	assert.True(t, isErr, text)
	assert.Empty(t, ts.assigned)

	c, _ := tools.NewTaskContractFromInput(mcpValidContract())
	ts.tasks[taskID].Metadata = map[string]any{tools.TaskMetaContract: c.ToMetadata()}
	isErr, text = call("goclaw_teams_tasks_assign", args)
	require.False(t, isErr, text)
	assert.Len(t, ts.assigned, 1)
}

func TestMCPTasksAssign_DisabledTeamUnchanged(t *testing.T) {
	ts, teamID, memberID, call := newContractMCPFixture(t, false)
	taskID := uuid.New()
	ts.tasks[taskID] = &store.TeamTaskData{BaseModel: store.BaseModel{ID: taskID}, TeamID: teamID}
	isErr, text := call("goclaw_teams_tasks_assign", map[string]any{
		"team_id": teamID.String(), "task_id": taskID.String(), "agent_id": memberID.String(),
	})
	require.False(t, isErr, text)
	assert.Len(t, ts.assigned, 1)
}

// nilTeamStore mô phỏng store trả team nil mà không kèm lỗi.
type nilTeamStore struct{ *contractTaskTeamStore }

func (s nilTeamStore) GetTeam(context.Context, uuid.UUID) (*store.TeamData, error) { return nil, nil }

func TestMCPTasks_NilTeamRejected(t *testing.T) {
	ts := &contractTaskTeamStore{fakeTeamStore: newFakeTeamStore(), tasks: map[uuid.UUID]*store.TeamTaskData{}}
	agents := newFakeAgentStore()
	memberID := uuid.New()
	agents.add(&store.AgentData{BaseModel: store.BaseModel{ID: memberID}, AgentKey: "member"})
	srv := newTestMCPServer()
	registerTeamsTasksCRUDTools(srv, nilTeamStore{ts}, agents)
	teamID := uuid.New()

	res := callTool(t, srv, "goclaw_teams_tasks_create", map[string]any{
		"team_id": teamID.String(), "subject": "Q3", "assign_to": memberID.String(),
	})
	assert.True(t, toolIsError(res), toolResultText(res))
	assert.Contains(t, toolResultText(res), "team not found")
	assert.Empty(t, ts.tasks)

	taskID := uuid.New()
	ts.tasks[taskID] = &store.TeamTaskData{BaseModel: store.BaseModel{ID: taskID}, TeamID: teamID, TaskType: "general"}
	res = callTool(t, srv, "goclaw_teams_tasks_assign", map[string]any{
		"team_id": teamID.String(), "task_id": taskID.String(), "agent_id": memberID.String(),
	})
	assert.True(t, toolIsError(res), toolResultText(res))
	assert.Contains(t, toolResultText(res), "team not found")
	assert.Empty(t, ts.assigned)
}

func TestMCPTasksCreate_UnknownTaskTypeRejected(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		ts, teamID, _, call := newContractMCPFixture(t, enabled)
		isErr, text := call("goclaw_teams_tasks_create", map[string]any{
			"team_id": teamID.String(), "subject": "Q3", "task_type": "generl",
		})
		assert.True(t, isErr, text)
		assert.Contains(t, text, "must be general, request, note, delegation or escalation")
		assert.Empty(t, ts.tasks)

		isErr, text = call("goclaw_teams_tasks_create", map[string]any{
			"team_id": teamID.String(), "subject": "Q3", "task_type": "note",
		})
		require.False(t, isErr, text)
		assert.Len(t, ts.tasks, 1)
	}
}

func TestMCPTasksCreate_EnabledTeamContractByTaskType(t *testing.T) {
	ts, teamID, memberID, call := newContractMCPFixture(t, true)
	// Task escalation chưa giao (luồng chờ người duyệt) vẫn tạo được.
	isErr, text := call("goclaw_teams_tasks_create", map[string]any{
		"team_id": teamID.String(), "subject": "Review", "task_type": "escalation",
	})
	require.False(t, isErr, text)
	require.Len(t, ts.tasks, 1)

	// Giao ngay cho member thì delegation và escalation đều cần contract.
	for _, typ := range []string{"delegation", "escalation"} {
		isErr, text = call("goclaw_teams_tasks_create", map[string]any{
			"team_id": teamID.String(), "subject": "Q3", "task_type": typ, "assign_to": memberID.String(),
		})
		assert.True(t, isErr, typ+": "+text)
		assert.Contains(t, text, "task contract")
		assert.Len(t, ts.tasks, 1)
	}
}
