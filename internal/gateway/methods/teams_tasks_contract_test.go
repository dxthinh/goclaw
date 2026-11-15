package methods

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// contractTaskStore bổ sung CreateTask cho stub của teams_tasks_human_test.go.
type contractTaskStore struct {
	*humanTaskStore
	created []*store.TeamTaskData
}

func (s *contractTaskStore) CreateTask(_ context.Context, task *store.TeamTaskData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	task.ID = uuid.New()
	cp := *task
	s.created = append(s.created, &cp)
	// Task mới thành task hiện hành để AssignTask của stub thao tác trên nó.
	s.task = &cp
	return nil
}

// contractAgentStore trả agent cho mọi UUID, đủ cho bước resolve agentId của assign.
type contractAgentStore struct{ store.AgentStore }

func (contractAgentStore) GetByID(_ context.Context, id uuid.UUID) (*store.AgentData, error) {
	return &store.AgentData{BaseModel: store.BaseModel{ID: id}, AgentKey: "member"}, nil
}

func newContractFixture(t *testing.T, enabled bool) (*humanTaskFixture, *contractTaskStore) {
	t.Helper()
	f := newHumanTaskFixture(t, store.TeamTaskStatusPending)
	if enabled {
		f.st.team.Settings = json.RawMessage(`{"task_contract":{"enabled":true}}`)
	}
	cs := &contractTaskStore{humanTaskStore: f.st}
	f.m = &TeamsMethods{teamStore: cs, agentStore: contractAgentStore{}}
	return f, cs
}

func validRPCContract() map[string]any {
	return map[string]any{
		"objective":    "Brief the board",
		"deliverables": []any{"q3.md"},
		"acceptance":   []any{"All regions"},
	}
}

func TestTaskCreate_EnabledTeamAssignRequiresContract(t *testing.T) {
	f, cs := newContractFixture(t, true)
	ok, code, msg := f.call(t, f.m.handleTaskCreate, map[string]any{
		"teamId": f.teamID.String(), "subject": "Q3", "assignTo": f.member.String(),
	})
	if ok || code != protocol.ErrInvalidRequest {
		t.Fatalf("expected INVALID_REQUEST, got ok=%v code=%s", ok, code)
	}
	if msg != i18n.T("en", i18n.MsgTaskContractRequired) {
		t.Errorf("message = %q", msg)
	}
	if len(cs.created) != 0 {
		t.Fatal("task must not be created")
	}
}

func TestTaskCreate_EnabledTeamBacklogWithoutContractAllowed(t *testing.T) {
	f, cs := newContractFixture(t, true)
	if ok, code, msg := f.call(t, f.m.handleTaskCreate, map[string]any{
		"teamId": f.teamID.String(), "subject": "Q3",
	}); !ok {
		t.Fatalf("backlog task must be created, got %s: %s", code, msg)
	}
	if len(cs.created) != 1 {
		t.Fatalf("created = %d", len(cs.created))
	}
}

func TestTaskCreate_ContractStoredAndAssigned(t *testing.T) {
	f, cs := newContractFixture(t, true)
	if ok, code, msg := f.call(t, f.m.handleTaskCreate, map[string]any{
		"teamId": f.teamID.String(), "subject": "Q3", "assignTo": f.member.String(),
		"contract": validRPCContract(),
	}); !ok {
		t.Fatalf("expected ok, got %s: %s", code, msg)
	}
	if len(cs.created) != 1 || len(f.st.assigned) != 1 {
		t.Fatalf("created=%d assigned=%d", len(cs.created), len(f.st.assigned))
	}
	c := tools.TaskContractFromTask(cs.created[0])
	if c == nil || c.Objective != "Brief the board" || c.Version != 1 {
		t.Fatalf("stored contract = %+v", c)
	}
}

func TestTaskCreate_InvalidContractRejectedEvenWhenDisabled(t *testing.T) {
	f, cs := newContractFixture(t, false)
	ok, code, msg := f.call(t, f.m.handleTaskCreate, map[string]any{
		"teamId": f.teamID.String(), "subject": "Q3", "contract": map[string]any{"objective": "x"},
	})
	if ok || code != protocol.ErrInvalidRequest || !strings.Contains(msg, "deliverables, acceptance") {
		t.Fatalf("got ok=%v code=%s msg=%q", ok, code, msg)
	}
	if len(cs.created) != 0 {
		t.Fatal("task must not be created")
	}
}

func TestTaskCreate_DisabledTeamUnchanged(t *testing.T) {
	f, cs := newContractFixture(t, false)
	if ok, code, msg := f.call(t, f.m.handleTaskCreate, map[string]any{
		"teamId": f.teamID.String(), "subject": "Q3", "assignTo": f.member.String(),
	}); !ok {
		t.Fatalf("expected ok, got %s: %s", code, msg)
	}
	if len(cs.created) != 1 || cs.created[0].Metadata != nil {
		t.Fatalf("created = %+v", cs.created)
	}
}

func TestTaskAssign_EnabledTeamRequiresContract(t *testing.T) {
	f, _ := newContractFixture(t, true)
	f.st.task.OwnerAgentID = nil
	params := map[string]any{"teamId": f.teamID.String(), "taskId": f.taskID.String(), "agentId": f.member.String()}

	ok, code, msg := f.call(t, f.m.handleTaskAssign, params)
	if ok || code != protocol.ErrInvalidRequest || msg != i18n.T("en", i18n.MsgTaskContractRequired) {
		t.Fatalf("got ok=%v code=%s msg=%q", ok, code, msg)
	}
	if len(f.st.assigned) != 0 {
		t.Fatal("task must not be assigned")
	}

	c, _ := tools.ParseTaskContract(validRPCContract())
	f.st.task.Metadata = map[string]any{tools.TaskMetaContract: c.ToMetadata()}
	if ok, code, msg := f.call(t, f.m.handleTaskAssign, params); !ok {
		t.Fatalf("assign with contract: %s %s", code, msg)
	}
	if len(f.st.assigned) != 1 {
		t.Fatalf("assigned = %v", f.st.assigned)
	}
}

func TestTaskAssign_RequestTaskWithoutContractAllowed(t *testing.T) {
	f, _ := newContractFixture(t, true)
	f.st.task.TaskType = "request"
	if ok, code, msg := f.call(t, f.m.handleTaskAssign, map[string]any{
		"teamId": f.teamID.String(), "taskId": f.taskID.String(), "agentId": f.member.String(),
	}); !ok {
		t.Fatalf("expected ok, got %s: %s", code, msg)
	}
}

func TestCleanTeamSettings_KeepsTaskContract(t *testing.T) {
	cleaned, err := cleanTeamSettings(map[string]any{
		"task_contract":      map[string]any{"enabled": true},
		"blocker_escalation": map[string]any{"enabled": false},
		"unknown_key":        "dropped",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !tools.ParseTaskContractConfig(cleaned).Enabled {
		t.Errorf("task_contract lost after save: %s", cleaned)
	}
	if tools.ParseBlockerEscalationConfig(cleaned).Enabled {
		t.Errorf("existing keys must survive: %s", cleaned)
	}
	if strings.Contains(string(cleaned), "unknown_key") {
		t.Errorf("unknown keys must be stripped: %s", cleaned)
	}
}

func TestTaskRetry_EnabledTeamRequiresContract(t *testing.T) {
	f, _ := newContractFixture(t, true)
	f.st.task.Status = store.TeamTaskStatusFailed
	params := map[string]any{"teamId": f.teamID.String(), "taskId": f.taskID.String(), "comment": "try again"}

	ok, code, msg := f.call(t, f.m.handleTaskRetry, params)
	if ok || code != protocol.ErrInvalidRequest || msg != i18n.T("en", i18n.MsgTaskContractRequired) {
		t.Fatalf("got ok=%v code=%s msg=%q", ok, code, msg)
	}
	if f.st.resets != 0 || len(f.st.assigned) != 0 || len(f.st.comments) != 0 {
		t.Fatal("rejected retry must not touch the task")
	}

	c, _ := tools.NewTaskContractFromInput(validRPCContract())
	f.st.task.Metadata = map[string]any{tools.TaskMetaContract: c.ToMetadata()}
	if ok, code, msg := f.call(t, f.m.handleTaskRetry, params); !ok {
		t.Fatalf("retry with contract: %s %s", code, msg)
	}
	if len(f.st.assigned) != 1 {
		t.Fatalf("assigned = %v", f.st.assigned)
	}
}

func TestTaskCreate_UnknownTaskTypeRejected(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		f, cs := newContractFixture(t, enabled)
		ok, code, msg := f.call(t, f.m.handleTaskCreate, map[string]any{
			"teamId": f.teamID.String(), "subject": "Q3", "taskType": "generl",
		})
		if ok || code != protocol.ErrInvalidRequest {
			t.Fatalf("enabled=%v: expected INVALID_REQUEST, got ok=%v code=%s", enabled, ok, code)
		}
		if msg != i18n.T("en", i18n.MsgTaskTypeInvalid, "generl") {
			t.Errorf("message = %q", msg)
		}
		if len(cs.created) != 0 {
			t.Fatal("task must not be created")
		}
	}
}
