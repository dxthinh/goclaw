package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

type needsStubTool struct{ name string }

func (s needsStubTool) Name() string                                          { return s.name }
func (s needsStubTool) Description() string                                   { return s.name }
func (s needsStubTool) Parameters() map[string]any                            { return nil }
func (s needsStubTool) Execute(context.Context, map[string]any) *tools.Result { return nil }

// byIDsAgentStore thêm GetByIDs cho fakeAgentStore.
type byIDsAgentStore struct{ *fakeAgentStore }

func (s byIDsAgentStore) GetByIDs(_ context.Context, ids []uuid.UUID) ([]store.AgentData, error) {
	var out []store.AgentData
	for _, id := range ids {
		if a, ok := s.byID[id]; ok {
			out = append(out, *a)
		}
	}
	return out, nil
}

// newNeedsMCPFixture: team bật contract, "member" bị deny nhóm web, "member2" có mọi tool.
func newNeedsMCPFixture(t *testing.T) (*contractTaskTeamStore, uuid.UUID, uuid.UUID, uuid.UUID, func(string, map[string]any) (bool, string)) {
	t.Helper()
	ctx := context.Background()
	ts := &contractTaskTeamStore{fakeTeamStore: newFakeTeamStore(), tasks: map[uuid.UUID]*store.TeamTaskData{}}
	team := &store.TeamData{Name: "t", LeadAgentID: uuid.New(), Settings: json.RawMessage(`{"task_contract":{"enabled":true}}`)}
	require.NoError(t, ts.CreateTeam(ctx, team))
	agents := byIDsAgentStore{newFakeAgentStore()}
	memberID, member2ID := uuid.New(), uuid.New()
	agents.add(&store.AgentData{BaseModel: store.BaseModel{ID: memberID}, AgentKey: "member", ToolsConfig: json.RawMessage(`{"deny":["group:web"]}`)})
	agents.add(&store.AgentData{BaseModel: store.BaseModel{ID: member2ID}, AgentKey: "member2"})
	require.NoError(t, ts.AddMember(ctx, team.ID, memberID, store.TeamRoleMember))
	require.NoError(t, ts.AddMember(ctx, team.ID, member2ID, store.TeamRoleMember))

	reg := tools.NewRegistry()
	for _, n := range []string{"web_search", "web_fetch", "exec"} {
		reg.Register(needsStubTool{name: n})
	}
	srv := newTestMCPServer()
	registerTeamsTasksCRUDTools(srv, ts, agents, tools.NewTeamCapabilityChecker(tools.NewPolicyEngine(&config.ToolsConfig{}), reg))
	call := func(name string, args map[string]any) (bool, string) {
		res := callTool(t, srv, name, args)
		return toolIsError(res), toolResultText(res)
	}
	return ts, team.ID, memberID, member2ID, call
}

func mcpNeedsWebContract() map[string]any {
	c := mcpValidContract()
	c["needs"] = []any{"web"}
	return c
}

func TestMCPTasksCreate_AssigneeMissingNeedsRejected(t *testing.T) {
	ts, teamID, memberID, member2ID, call := newNeedsMCPFixture(t)
	isErr, text := call("goclaw_teams_tasks_create", map[string]any{
		"team_id": teamID.String(), "subject": "Find sources", "assign_to": memberID.String(), "contract": mcpNeedsWebContract(),
	})
	assert.True(t, isErr, text)
	assert.Contains(t, text, "member2")
	assert.Empty(t, ts.tasks)

	isErr, text = call("goclaw_teams_tasks_create", map[string]any{
		"team_id": teamID.String(), "subject": "Find sources", "assign_to": member2ID.String(), "contract": mcpNeedsWebContract(),
	})
	require.False(t, isErr, text)
	assert.Len(t, ts.assigned, 1)
}

func TestMCPTasksAssign_AssigneeMissingNeedsRejected(t *testing.T) {
	ts, teamID, memberID, member2ID, call := newNeedsMCPFixture(t)
	taskID := uuid.New()
	c, _ := tools.NewTaskContractFromInput(mcpNeedsWebContract())
	ts.tasks[taskID] = &store.TeamTaskData{BaseModel: store.BaseModel{ID: taskID}, TeamID: teamID, TaskType: "general",
		Metadata: map[string]any{tools.TaskMetaContract: c.ToMetadata()}}

	isErr, text := call("goclaw_teams_tasks_assign", map[string]any{"team_id": teamID.String(), "task_id": taskID.String(), "agent_id": memberID.String()})
	assert.True(t, isErr, text)
	assert.Contains(t, text, "web")
	assert.Empty(t, ts.assigned)

	isErr, text = call("goclaw_teams_tasks_assign", map[string]any{"team_id": teamID.String(), "task_id": taskID.String(), "agent_id": member2ID.String()})
	require.False(t, isErr, text)
	assert.True(t, slices.Contains(ts.assigned, taskID))
}
