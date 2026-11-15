package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// capStubTool là tool rỗng chỉ để đăng ký tên vào registry.
type capStubTool struct{ name string }

func (s capStubTool) Name() string                                    { return s.name }
func (s capStubTool) Description() string                             { return s.name }
func (s capStubTool) Parameters() map[string]any                      { return nil }
func (s capStubTool) Execute(context.Context, map[string]any) *Result { return NewResult("") }

// capAgentStore trả agent theo ID cho GetByIDs và đếm số lần gọi.
type capAgentStore struct {
	store.AgentStore
	agents []store.AgentData
	calls  int
}

func (s *capAgentStore) GetByIDs(_ context.Context, ids []uuid.UUID) ([]store.AgentData, error) {
	s.calls++
	var out []store.AgentData
	for _, a := range s.agents {
		if slices.Contains(ids, a.ID) {
			out = append(out, a)
		}
	}
	return out, nil
}

// newCapabilityChecker dựng registry có nhóm web, fs, runtime, team (không có
// browser nên nhóm ui không có tool nào) và PolicyEngine không có policy chung.
func newCapabilityChecker(t *testing.T) *TeamCapabilityChecker {
	t.Helper()
	reg := NewRegistry()
	for _, n := range []string{"web_search", "web_fetch", "read_file", "write_file", "list_files", "edit", "exec", "wait", "team_tasks"} {
		reg.Register(capStubTool{name: n})
	}
	c := NewTeamCapabilityChecker(NewPolicyEngine(&config.ToolsConfig{}), reg)
	if c == nil {
		t.Fatal("checker must not be nil")
	}
	return c
}

func capAgent(id uuid.UUID, key, toolsConfig string) store.AgentData {
	a := store.AgentData{BaseModel: store.BaseModel{ID: id}, AgentKey: key, Provider: "openai"}
	if toolsConfig != "" {
		a.ToolsConfig = json.RawMessage(toolsConfig)
	}
	return a
}

var (
	capLeadID   = uuid.MustParse("10000000-0000-0000-0000-000000000001")
	capWebID    = uuid.MustParse("10000000-0000-0000-0000-000000000002")
	capNoWebID  = uuid.MustParse("10000000-0000-0000-0000-000000000003")
	capNarrowID = uuid.MustParse("10000000-0000-0000-0000-000000000004")
)

// capTeam: researcher có mọi tool, coder bị deny nhóm web, writer chỉ được
// allow read_file (cộng ba tool file của workspace team).
func capTeam(settings string) (*store.TeamData, []store.TeamMemberData, *capAgentStore) {
	team := &store.TeamData{BaseModel: store.BaseModel{ID: testTeamID}, LeadAgentID: capLeadID}
	if settings != "" {
		team.Settings = json.RawMessage(settings)
	}
	members := []store.TeamMemberData{
		{AgentID: capLeadID, Role: store.TeamRoleLead, AgentKey: "lead"},
		{AgentID: capWebID, Role: store.TeamRoleMember, AgentKey: "researcher"},
		{AgentID: capNoWebID, Role: store.TeamRoleMember, AgentKey: "coder"},
		{AgentID: capNarrowID, Role: store.TeamRoleMember, AgentKey: "writer"},
	}
	agents := &capAgentStore{agents: []store.AgentData{
		capAgent(capLeadID, "lead", ""),
		capAgent(capWebID, "researcher", ""),
		capAgent(capNoWebID, "coder", `{"deny":["group:web"]}`),
		capAgent(capNarrowID, "writer", `{"allow":["read_file"]}`),
	}}
	return team, members, agents
}

func TestCapabilityEffectiveTools(t *testing.T) {
	c := newCapabilityChecker(t)
	_, _, agents := capTeam("")
	coder := c.EffectiveTools(&agents.agents[2], nil)
	if coder["web_search"] || !coder["exec"] || !coder["read_file"] {
		t.Errorf("coder tools = %v", coder)
	}
	writer := c.EffectiveTools(&agents.agents[3], nil)
	for _, n := range TeamWorkspaceTools {
		if !writer[n] {
			t.Errorf("writer must get workspace tool %s, got %v", n, writer)
		}
	}
	if writer["edit"] || writer["exec"] || writer["team_tasks"] {
		t.Errorf("writer must only have file tools, got %v", writer)
	}
}

func TestCapabilityMissingNeeds(t *testing.T) {
	c := newCapabilityChecker(t)
	_, _, agents := capTeam("")
	coder, writer := &agents.agents[2], &agents.agents[3]
	if got := c.MissingNeeds(coder, []string{"web", "runtime"}, nil); !slices.Equal(got, []string{"web"}) {
		t.Errorf("coder missing = %v, want [web]", got)
	}
	if got := c.MissingNeeds(coder, []string{"group:runtime", "exec"}, nil); len(got) != 0 {
		t.Errorf("group: prefix and tool names must resolve, got %v", got)
	}
	// Nhóm fs gồm cả edit: writer chỉ có ba tool file nên chưa đủ nhóm.
	if got := c.MissingNeeds(writer, []string{"fs", "read_file"}, nil); !slices.Equal(got, []string{"fs"}) {
		t.Errorf("writer missing = %v, want [fs]", got)
	}
	// Nhóm không có tool nào trong registry thì không ai có.
	if got := c.MissingNeeds(&agents.agents[1], []string{"ui"}, nil); !slices.Equal(got, []string{"ui"}) {
		t.Errorf("empty group must be missing, got %v", got)
	}
	if got := c.MembersSatisfying(agents.agents, []string{"web"}, nil, capLeadID); !slices.Equal(got, []string{"researcher"}) {
		t.Errorf("satisfying = %v, want [researcher]", got)
	}
}

func TestCapabilityValidateNeeds(t *testing.T) {
	c := newCapabilityChecker(t)
	if err := c.ValidateNeeds([]string{"web", "group:fs", "exec"}); err != nil {
		t.Fatalf("valid names rejected: %v", err)
	}
	err := c.ValidateNeeds([]string{"web", "internet", "magic_tool"})
	if err == nil || !strings.Contains(err.Error(), "internet, magic_tool") || !strings.Contains(err.Error(), "runtime") {
		t.Fatalf("unknown names must be listed with valid groups, got %v", err)
	}
	var nilChecker *TeamCapabilityChecker
	if err := nilChecker.ValidateNeeds([]string{"anything"}); err != nil {
		t.Errorf("nil checker must skip validation, got %v", err)
	}
}

func TestCapabilityCheckTaskNeeds(t *testing.T) {
	ctx := context.Background()
	c := newCapabilityChecker(t)
	needsWeb := &TaskContract{Objective: "x", Deliverables: []string{"y"}, Acceptance: []string{"z"}, Needs: []string{"web"}}

	t.Run("assignee lacking web is rejected with members that have it", func(t *testing.T) {
		team, members, agents := capTeam(contractEnabledSettings)
		err := c.CheckTaskNeeds(ctx, agents, team, members, needsWeb, capNoWebID)
		var ne *TaskNeedsError
		if !errors.As(err, &ne) {
			t.Fatalf("want TaskNeedsError, got %v", err)
		}
		if ne.Assignee != "coder" || !slices.Equal(ne.Missing, []string{"web"}) || !slices.Equal(ne.Satisfying, []string{"researcher"}) {
			t.Errorf("error = %+v", ne)
		}
		if !strings.Contains(err.Error(), "researcher") {
			t.Errorf("message must name the members that have the tools: %s", err)
		}
	})
	t.Run("assignee with web passes", func(t *testing.T) {
		team, members, agents := capTeam(contractEnabledSettings)
		if err := c.CheckTaskNeeds(ctx, agents, team, members, needsWeb, capWebID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("nobody has the group", func(t *testing.T) {
		team, members, agents := capTeam(contractEnabledSettings)
		needsUI := &TaskContract{Needs: []string{"ui"}}
		err := c.CheckTaskNeeds(ctx, agents, team, members, needsUI, capWebID)
		var ne *TaskNeedsError
		if !errors.As(err, &ne) || len(ne.Satisfying) != 0 || !strings.Contains(err.Error(), "admin") {
			t.Fatalf("want no-member error asking for an admin, got %v", err)
		}
	})
	t.Run("unknown need name", func(t *testing.T) {
		team, members, agents := capTeam(contractEnabledSettings)
		err := c.CheckTaskNeeds(ctx, agents, team, members, &TaskContract{Needs: []string{"internet"}}, capWebID)
		var ne *TaskNeedsError
		if err == nil || errors.As(err, &ne) || agents.calls != 0 {
			t.Fatalf("unknown name must fail validation before loading agents, got %v (calls=%d)", err, agents.calls)
		}
	})
	t.Run("no check without needs, without contract or on a disabled team", func(t *testing.T) {
		team, members, agents := capTeam(contractEnabledSettings)
		disabled, _, _ := capTeam("")
		cases := []struct {
			team     *store.TeamData
			contract *TaskContract
		}{
			{team, &TaskContract{Objective: "x"}},
			{team, nil},
			{disabled, needsWeb},
		}
		for i, tc := range cases {
			if err := c.CheckTaskNeeds(ctx, agents, tc.team, members, tc.contract, capNoWebID); err != nil {
				t.Errorf("case %d: %v", i, err)
			}
		}
		if agents.calls != 0 {
			t.Errorf("no query expected, got %d GetByIDs calls", agents.calls)
		}
	})
	t.Run("nil checker skips", func(t *testing.T) {
		team, members, agents := capTeam(contractEnabledSettings)
		var nilChecker *TeamCapabilityChecker
		if err := nilChecker.CheckTaskNeeds(ctx, agents, team, members, needsWeb, capNoWebID); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCapabilityMemberToolGroupDiff(t *testing.T) {
	c := newCapabilityChecker(t)
	_, _, agents := capTeam("")
	diff := c.MemberToolGroupDiff(agents.agents[1:], nil)
	if !slices.Equal(diff[capWebID], []string{"fs", "runtime", "team", "web"}) {
		t.Errorf("researcher = %v", diff[capWebID])
	}
	if !slices.Equal(diff[capNoWebID], []string{"fs", "runtime", "team"}) {
		t.Errorf("coder = %v", diff[capNoWebID])
	}
	if got, ok := diff[capNarrowID]; !ok || len(got) != 0 {
		t.Errorf("writer must be listed with no differing groups, got %v (present=%v)", got, ok)
	}
	// Mọi member giống nhau thì không có gì để in.
	same := []store.AgentData{agents.agents[1], capAgent(uuid.New(), "twin", "")}
	if diff := c.MemberToolGroupDiff(same, nil); diff != nil {
		t.Errorf("identical members must give nil, got %v", diff)
	}
}

// capabilityBackend gắn cổng needs thật vào mockBackend.
type capabilityBackend struct {
	*mockBackend
	checker *TeamCapabilityChecker
	agents  store.AgentStore
}

func (b *capabilityBackend) CheckTaskNeeds(ctx context.Context, team *store.TeamData, members []store.TeamMemberData, contract *TaskContract, assigneeID uuid.UUID) error {
	return b.checker.CheckTaskNeeds(ctx, b.agents, team, members, contract, assigneeID)
}

func (b *capabilityBackend) AssigneeToolWarning(context.Context, *store.TeamData, uuid.UUID) string {
	return ""
}

func TestCreateTaskNeeds(t *testing.T) {
	run := func(t *testing.T, settings string, contract map[string]any) (*Result, int) {
		t.Helper()
		mb, _, _, _, ctx := newTestTeamSetup()
		mb.team.Settings = json.RawMessage(settings)
		mb.taskStore.team = mb.team
		agents := &capAgentStore{agents: []store.AgentData{
			capAgent(testLeadID, "lead-agent", ""),
			capAgent(testMemberID, "member-agent", `{"deny":["group:web"]}`),
			capAgent(testMember2ID, "member2-agent", ""),
		}}
		tool := NewTeamTasksTool(&capabilityBackend{mockBackend: mb, checker: newCapabilityChecker(t), agents: agents}, FullTeamPolicy{})
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)
		result := tool.Execute(ctx, map[string]any{
			"action": "create", "subject": "Find sources", "assignee": "member-agent", "contract": contract,
		})
		mb.taskStore.mu.Lock()
		defer mb.taskStore.mu.Unlock()
		return result, len(mb.taskStore.tasks)
	}
	withNeeds := validContractArgs()
	withNeeds["needs"] = []any{"web"}

	result, created := run(t, contractEnabledSettings, withNeeds)
	if !result.IsError || created != 0 {
		t.Fatalf("assignee without web must be rejected, got %q (created=%d)", result.ForLLM, created)
	}
	if !strings.Contains(result.ForLLM, "member2-agent") || !strings.Contains(result.ForLLM, "web") {
		t.Errorf("error must name the missing group and the member that has it: %s", result.ForLLM)
	}

	if result, created := run(t, contractEnabledSettings, validContractArgs()); result.IsError || created != 1 {
		t.Fatalf("contract without needs must not be checked: %q", result.ForLLM)
	}
	if result, created := run(t, "", withNeeds); result.IsError || created != 1 {
		t.Fatalf("team without task_contract must not be checked: %q", result.ForLLM)
	}
}

func TestRetryTaskNeeds(t *testing.T) {
	mb, _, leadID, memberID, ctx := newTestTeamSetup()
	mb.team.Settings = json.RawMessage(contractEnabledSettings)
	mb.taskStore.team = mb.team
	agents := &capAgentStore{agents: []store.AgentData{
		capAgent(testLeadID, "lead-agent", ""),
		capAgent(testMemberID, "member-agent", `{"deny":["group:web"]}`),
		capAgent(testMember2ID, "member2-agent", ""),
	}}
	tool := NewTeamTasksTool(&capabilityBackend{mockBackend: mb, checker: newCapabilityChecker(t), agents: agents}, FullTeamPolicy{})
	taskID := makeTask(mb, testTeamID, memberID, store.TeamTaskStatusFailed)
	contract, _ := NewTaskContractFromInput(validContractArgs())
	contract.Needs = []string{"web"}
	mb.taskStore.mu.Lock()
	mb.taskStore.tasks[taskID].Metadata = map[string]any{TaskMetaContract: contract.ToMetadata()}
	mb.taskStore.mu.Unlock()

	ctx = WithTaskActionFlags(store.WithAgentID(ctx, leadID), &TaskActionFlags{})
	result := tool.Execute(ctx, map[string]any{"action": "retry", "task_id": taskID.String()})
	if !result.IsError || !strings.Contains(result.ForLLM, "member2-agent") {
		t.Fatalf("retry to an owner without web must fail naming member2-agent, got %q", result.ForLLM)
	}
	mb.mu.Lock()
	defer mb.mu.Unlock()
	if len(mb.dispatches) != 0 {
		t.Error("rejected retry must not dispatch")
	}
}

// capTenantCfgs trả danh sách tool tenant đã tắt cho ListDisabled.
type capTenantCfgs struct {
	store.BuiltinToolTenantConfigStore
	disabled []string
	calls    int
}

func (s *capTenantCfgs) ListDisabled(context.Context, uuid.UUID) ([]string, error) {
	s.calls++
	return s.disabled, nil
}

// TestCapabilityTenantDisabledTools: tool tenant đã tắt bị bỏ lúc chạy
// (buildFilteredTools) nên cũng phải bị bỏ khi kiểm needs.
func TestCapabilityTenantDisabledTools(t *testing.T) {
	ctx := context.Background()
	cfgs := &capTenantCfgs{disabled: []string{"web_fetch"}}
	c := newCapabilityChecker(t).WithTenantToolConfigs(cfgs)
	team, members, agents := capTeam(contractEnabledSettings)
	for i := range agents.agents {
		agents.agents[i].TenantID = testTenantID
	}
	needsWeb := &TaskContract{Needs: []string{"web"}}

	err := c.CheckTaskNeeds(ctx, agents, team, members, needsWeb, capWebID)
	var ne *TaskNeedsError
	if !errors.As(err, &ne) || !slices.Equal(ne.Missing, []string{"web"}) || len(ne.Satisfying) != 0 {
		t.Fatalf("researcher must lack web when the tenant disables web_fetch, got %v", err)
	}
	if cfgs.calls != 1 {
		t.Errorf("tenant exclusions must be read once per check, got %d", cfgs.calls)
	}
	// Tool khác của nhóm khác không bị ảnh hưởng.
	if err := c.CheckTaskNeeds(ctx, agents, team, members, &TaskContract{Needs: []string{"web_search", "runtime"}}, capWebID); err != nil {
		t.Errorf("web_search and runtime stay available, got %v", err)
	}
	// Không có needs thì không đọc cấu hình tenant.
	cfgs.calls = 0
	if err := c.CheckTaskNeeds(ctx, agents, team, members, &TaskContract{}, capWebID); err != nil || cfgs.calls != 0 {
		t.Errorf("no needs must not read tenant config, err=%v calls=%d", err, cfgs.calls)
	}
	if diff := c.MemberToolGroupDiff(agents.agents[1:], map[string]bool{"web_fetch": true}); slices.Contains(diff[capWebID], "web") {
		t.Errorf("web must not be listed for anyone once web_fetch is disabled, got %v", diff)
	}
}
