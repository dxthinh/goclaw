package methods

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

type needsStubTool struct{ name string }

func (s needsStubTool) Name() string                                          { return s.name }
func (s needsStubTool) Description() string                                   { return s.name }
func (s needsStubTool) Parameters() map[string]any                            { return nil }
func (s needsStubTool) Execute(context.Context, map[string]any) *tools.Result { return nil }

// needsTeamStore thêm ListMembers cho stub contract.
type needsTeamStore struct {
	*contractTaskStore
	members []store.TeamMemberData
}

func (s *needsTeamStore) ListMembers(context.Context, uuid.UUID) ([]store.TeamMemberData, error) {
	return s.members, nil
}

// needsAgentStore: member bị deny nhóm web, member2 có mọi tool.
type needsAgentStore struct {
	contractAgentStore
	agents []store.AgentData
}

func (s needsAgentStore) GetByIDs(_ context.Context, ids []uuid.UUID) ([]store.AgentData, error) {
	var out []store.AgentData
	for _, a := range s.agents {
		if slices.Contains(ids, a.ID) {
			out = append(out, a)
		}
	}
	return out, nil
}

func newNeedsFixture(t *testing.T, withChecker bool) (*humanTaskFixture, *contractTaskStore, uuid.UUID) {
	t.Helper()
	f, cs := newContractFixture(t, true)
	member2 := uuid.New()
	ns := &needsTeamStore{contractTaskStore: cs, members: []store.TeamMemberData{
		{AgentID: f.leadID, Role: store.TeamRoleLead},
		{AgentID: f.member, Role: store.TeamRoleMember},
		{AgentID: member2, Role: store.TeamRoleMember},
	}}
	as := needsAgentStore{agents: []store.AgentData{
		{BaseModel: store.BaseModel{ID: f.leadID}, AgentKey: "lead"},
		{BaseModel: store.BaseModel{ID: f.member}, AgentKey: "member", ToolsConfig: json.RawMessage(`{"deny":["group:web"]}`)},
		{BaseModel: store.BaseModel{ID: member2}, AgentKey: "member2"},
	}}
	f.m = &TeamsMethods{teamStore: ns, agentStore: as}
	if withChecker {
		reg := tools.NewRegistry()
		for _, n := range []string{"web_search", "web_fetch", "read_file", "exec"} {
			reg.Register(needsStubTool{name: n})
		}
		f.m.SetToolCapabilities(tools.NewTeamCapabilityChecker(tools.NewPolicyEngine(&config.ToolsConfig{}), reg))
	}
	return f, cs, member2
}

func needsWebContract() map[string]any {
	c := validRPCContract()
	c["needs"] = []any{"web"}
	return c
}

func TestTaskCreate_AssigneeMissingNeedsRejected(t *testing.T) {
	f, cs, _ := newNeedsFixture(t, true)
	ok, code, msg := f.call(t, f.m.handleTaskCreate, map[string]any{
		"teamId": f.teamID.String(), "subject": "Find sources", "assignTo": f.member.String(), "contract": needsWebContract(),
	})
	if ok || code != protocol.ErrInvalidRequest {
		t.Fatalf("expected INVALID_REQUEST, got ok=%v code=%s", ok, code)
	}
	if want := i18n.T("en", i18n.MsgTaskNeedsMissing, "member", "web", "member2"); msg != want {
		t.Errorf("message = %q, want %q", msg, want)
	}
	if len(cs.created) != 0 {
		t.Fatal("task must not be created")
	}
}

func TestTaskAssign_NeedsCheckedAgainstAssignee(t *testing.T) {
	f, _, member2 := newNeedsFixture(t, true)
	f.st.task.OwnerAgentID = nil
	c, _ := tools.NewTaskContractFromInput(needsWebContract())
	f.st.task.Metadata = map[string]any{tools.TaskMetaContract: c.ToMetadata()}

	ok, code, msg := f.call(t, f.m.handleTaskAssign, map[string]any{
		"teamId": f.teamID.String(), "taskId": f.taskID.String(), "agentId": f.member.String(),
	})
	if ok || code != protocol.ErrInvalidRequest || msg != i18n.T("en", i18n.MsgTaskNeedsMissing, "member", "web", "member2") {
		t.Fatalf("got ok=%v code=%s msg=%q", ok, code, msg)
	}
	if len(f.st.assigned) != 0 {
		t.Fatal("task must not be assigned")
	}
	if ok, code, msg := f.call(t, f.m.handleTaskAssign, map[string]any{
		"teamId": f.teamID.String(), "taskId": f.taskID.String(), "agentId": member2.String(),
	}); !ok {
		t.Fatalf("member2 has web, got %s: %s", code, msg)
	}
}

func TestTaskRetry_NeedsCheckedAgainstAssignee(t *testing.T) {
	f, _, _ := newNeedsFixture(t, true)
	f.st.task.Status = store.TeamTaskStatusFailed
	c, _ := tools.NewTaskContractFromInput(needsWebContract())
	f.st.task.Metadata = map[string]any{tools.TaskMetaContract: c.ToMetadata()}
	ok, code, _ := f.call(t, f.m.handleTaskRetry, map[string]any{
		"teamId": f.teamID.String(), "taskId": f.taskID.String(), "comment": "again",
	})
	if ok || code != protocol.ErrInvalidRequest {
		t.Fatalf("retry to an assignee without web must fail, got ok=%v code=%s", ok, code)
	}
	if f.st.resets != 0 || len(f.st.assigned) != 0 {
		t.Fatal("rejected retry must not touch the task")
	}
}

func TestTaskAssign_NeedsSkippedWithoutChecker(t *testing.T) {
	f, _, _ := newNeedsFixture(t, false)
	f.st.task.OwnerAgentID = nil
	c, _ := tools.NewTaskContractFromInput(needsWebContract())
	f.st.task.Metadata = map[string]any{tools.TaskMetaContract: c.ToMetadata()}
	if ok, code, msg := f.call(t, f.m.handleTaskAssign, map[string]any{
		"teamId": f.teamID.String(), "taskId": f.taskID.String(), "agentId": f.member.String(),
	}); !ok {
		t.Fatalf("without checker the needs check is skipped, got %s: %s", code, msg)
	}
}
