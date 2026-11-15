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
	if got := c.MissingNeeds(coder, []string{"web", "runtime"}, nil, ""); !slices.Equal(got, []string{"web"}) {
		t.Errorf("coder missing = %v, want [web]", got)
	}
	if got := c.MissingNeeds(coder, []string{"group:runtime", "exec"}, nil, ""); len(got) != 0 {
		t.Errorf("group: prefix and tool names must resolve, got %v", got)
	}
	// Nhóm fs gồm cả edit: writer chỉ có ba tool file nên chưa đủ nhóm.
	if got := c.MissingNeeds(writer, []string{"fs", "read_file"}, nil, ""); !slices.Equal(got, []string{"fs"}) {
		t.Errorf("writer missing = %v, want [fs]", got)
	}
	// Nhóm không có tool nào trong registry thì không ai có.
	if got := c.MissingNeeds(&agents.agents[1], []string{"ui"}, nil, ""); !slices.Equal(got, []string{"ui"}) {
		t.Errorf("empty group must be missing, got %v", got)
	}
	if got := c.MembersSatisfying(agents.agents, []string{"web"}, nil, "", capLeadID); !slices.Equal(got, []string{"researcher"}) {
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
		err := c.CheckTaskNeeds(ctx, agents, team, members, needsWeb, capNoWebID, "")
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
		if err := c.CheckTaskNeeds(ctx, agents, team, members, needsWeb, capWebID, ""); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("nobody has the group", func(t *testing.T) {
		team, members, agents := capTeam(contractEnabledSettings)
		needsUI := &TaskContract{Needs: []string{"ui"}}
		err := c.CheckTaskNeeds(ctx, agents, team, members, needsUI, capWebID, "")
		var ne *TaskNeedsError
		if !errors.As(err, &ne) || len(ne.Satisfying) != 0 || !strings.Contains(err.Error(), "admin") {
			t.Fatalf("want no-member error asking for an admin, got %v", err)
		}
	})
	t.Run("unknown need name", func(t *testing.T) {
		team, members, agents := capTeam(contractEnabledSettings)
		err := c.CheckTaskNeeds(ctx, agents, team, members, &TaskContract{Needs: []string{"internet"}}, capWebID, "")
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
			if err := c.CheckTaskNeeds(ctx, agents, tc.team, members, tc.contract, capNoWebID, ""); err != nil {
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
		if err := nilChecker.CheckTaskNeeds(ctx, agents, team, members, needsWeb, capNoWebID, ""); err != nil {
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

func (b *capabilityBackend) CheckAssignee(ctx context.Context, team *store.TeamData, members []store.TeamMemberData, contract *TaskContract, assigneeID uuid.UUID, channelType string, _ bool) (string, error) {
	return "", b.checker.CheckTaskNeeds(ctx, b.agents, team, members, contract, assigneeID, channelType)
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

	err := c.CheckTaskNeeds(ctx, agents, team, members, needsWeb, capWebID, "")
	var ne *TaskNeedsError
	if !errors.As(err, &ne) || !slices.Equal(ne.Missing, []string{"web"}) || len(ne.Satisfying) != 0 {
		t.Fatalf("researcher must lack web when the tenant disables web_fetch, got %v", err)
	}
	if cfgs.calls != 1 {
		t.Errorf("tenant exclusions must be read once per check, got %d", cfgs.calls)
	}
	// Tool khác của nhóm khác không bị ảnh hưởng.
	if err := c.CheckTaskNeeds(ctx, agents, team, members, &TaskContract{Needs: []string{"web_search", "runtime"}}, capWebID, ""); err != nil {
		t.Errorf("web_search and runtime stay available, got %v", err)
	}
	// Không có needs thì không đọc cấu hình tenant.
	cfgs.calls = 0
	if err := c.CheckTaskNeeds(ctx, agents, team, members, &TaskContract{}, capWebID, ""); err != nil || cfgs.calls != 0 {
		t.Errorf("no needs must not read tenant config, err=%v calls=%d", err, cfgs.calls)
	}
	if diff := c.MemberToolGroupDiff(agents.agents[1:], map[string]bool{"web_fetch": true}); slices.Contains(diff[capWebID], "web") {
		t.Errorf("web must not be listed for anyone once web_fetch is disabled, got %v", diff)
	}
}

func (s *capAgentStore) GetByID(_ context.Context, id uuid.UUID) (*store.AgentData, error) {
	for i := range s.agents {
		if s.agents[i].ID == id {
			a := s.agents[i]
			return &a, nil
		}
	}
	return nil, errors.New("agent not found")
}

// TestManagerCheckAssigneeReadsTenantConfigOnce: cổng needs và cảnh báo
// team_tasks dùng chung một lần đọc tool bị tắt theo tenant.
func TestManagerCheckAssigneeReadsTenantConfigOnce(t *testing.T) {
	ctx := context.Background()
	team, members, agents := capTeam(contractEnabledSettings)
	for i := range agents.agents {
		agents.agents[i].TenantID = testTenantID
	}
	cfgs := &capTenantCfgs{}
	c := newCapabilityChecker(t)
	m := NewTeamToolManager(nil, agents, nil, t.TempDir())
	m.SetToolPolicy(c.pe, c.reg, cfgs)

	// Có needs: writer đủ read_file nhưng không có team_tasks nên bị cảnh báo.
	w, err := m.CheckAssignee(ctx, team, members, &TaskContract{Needs: []string{"read_file"}}, capNarrowID, "", true)
	if err != nil || !strings.Contains(w, "hides team_tasks") || cfgs.calls != 1 {
		t.Fatalf("with needs: warning=%q err=%v calls=%d, want warning and 1 read", w, err, cfgs.calls)
	}
	// Không có needs: vẫn cảnh báo, vẫn chỉ một lần đọc.
	cfgs.calls = 0
	w, err = m.CheckAssignee(ctx, team, members, nil, capNarrowID, "", true)
	if err != nil || w == "" || cfgs.calls != 1 {
		t.Fatalf("without needs: warning=%q err=%v calls=%d", w, err, cfgs.calls)
	}
	// Tenant tắt team_tasks: researcher cũng bị cảnh báo.
	cfgs.calls, cfgs.disabled = 0, []string{"team_tasks"}
	if w, _ := m.CheckAssignee(ctx, team, members, &TaskContract{Needs: []string{"web"}}, capWebID, "", true); w == "" || cfgs.calls != 1 {
		t.Errorf("tenant-disabled team_tasks must warn with one read, warning=%q calls=%d", w, cfgs.calls)
	}
	// Retry (không cảnh báo) và team chưa bật: không đọc thừa.
	cfgs.calls = 0
	if _, err := m.CheckAssignee(ctx, team, members, nil, capWebID, "", false); err != nil || cfgs.calls != 0 {
		t.Errorf("no needs and no warning must not read tenant config, calls=%d", cfgs.calls)
	}
	disabledTeam, _, _ := capTeam("")
	if w, err := m.CheckAssignee(ctx, disabledTeam, members, &TaskContract{Needs: []string{"web"}}, capNoWebID, "", true); w != "" || err != nil || cfgs.calls != 0 {
		t.Errorf("disabled team must be untouched, w=%q err=%v calls=%d", w, err, cfgs.calls)
	}
}

// capChannelTool là tool chỉ dùng được trên một số loại kênh (ChannelAware).
type capChannelTool struct {
	capStubTool
	channels []string
}

func (t capChannelTool) RequiredChannelTypes() []string { return t.channels }

// newChannelCapabilityChecker có nhóm messaging gồm tool của nhiều kênh:
// message (mọi kênh), create_forum_topic (telegram), list_group_members (feishu).
func newChannelCapabilityChecker(t *testing.T) *TeamCapabilityChecker {
	t.Helper()
	reg := NewRegistry()
	reg.Register(capStubTool{name: "message"})
	reg.Register(capStubTool{name: "web_search"})
	reg.Register(capChannelTool{capStubTool{name: "create_forum_topic"}, []string{"telegram"}})
	reg.Register(capChannelTool{capStubTool{name: "list_group_members"}, []string{"feishu"}})
	return NewTeamCapabilityChecker(NewPolicyEngine(&config.ToolsConfig{}), reg)
}

// TestCapabilityChannelAwareNeeds: tool ChannelAware bị ẩn lúc chạy khi kênh
// của task không khớp, nên needs nêu tool đó phải bị chặn trên kênh khác.
func TestCapabilityChannelAwareNeeds(t *testing.T) {
	c := newChannelCapabilityChecker(t)
	ag := capAgent(uuid.New(), "member", "")
	forum := []string{"create_forum_topic"}
	if got := c.MissingNeeds(&ag, forum, nil, "feishu"); !slices.Equal(got, forum) {
		t.Errorf("telegram-only tool on a feishu task must be missing, got %v", got)
	}
	if got := c.MissingNeeds(&ag, forum, nil, "telegram"); len(got) != 0 {
		t.Errorf("telegram-only tool on a telegram task must pass, got %v", got)
	}
	if got := c.MissingNeeds(&ag, forum, nil, ""); len(got) != 0 {
		t.Errorf("unknown channel must not filter, got %v", got)
	}
	// Nhóm messaging chỉ bắt buộc phần tool của kênh task.
	for _, ch := range []string{"telegram", "feishu", ""} {
		if got := c.MissingNeeds(&ag, []string{"messaging"}, nil, ch); len(got) != 0 {
			t.Errorf("messaging on %q must pass for a full agent, got %v", ch, got)
		}
	}
	// Bị deny phần của kênh thì thiếu nhóm trên kênh đó, không thiếu trên kênh khác.
	denyForum := capAgent(uuid.New(), "no-forum", `{"deny":["create_forum_topic"]}`)
	if got := c.MissingNeeds(&denyForum, []string{"messaging"}, nil, "telegram"); !slices.Equal(got, []string{"messaging"}) {
		t.Errorf("telegram messaging without create_forum_topic must be missing, got %v", got)
	}
	if got := c.MissingNeeds(&denyForum, []string{"messaging"}, nil, "feishu"); len(got) != 0 {
		t.Errorf("feishu messaging does not need create_forum_topic, got %v", got)
	}
}

func TestCreateTaskNeeds_ChannelAware(t *testing.T) {
	run := func(t *testing.T, channel, channelType string) *Result {
		t.Helper()
		mb, _, _, _, ctx := newTestTeamSetup()
		mb.team.Settings = json.RawMessage(contractEnabledSettings)
		mb.taskStore.team = mb.team
		agents := &capAgentStore{agents: []store.AgentData{
			capAgent(testLeadID, "lead-agent", ""),
			capAgent(testMemberID, "member-agent", ""),
			capAgent(testMember2ID, "member2-agent", ""),
		}}
		tool := NewTeamTasksTool(&capabilityBackend{mockBackend: mb, checker: newChannelCapabilityChecker(t), agents: agents}, FullTeamPolicy{})
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithToolChannelType(WithToolChannel(WithPendingTeamDispatch(ctx, ptd), channel), channelType)
		contract := validContractArgs()
		contract["needs"] = []any{"create_forum_topic"}
		return tool.Execute(ctx, map[string]any{
			"action": "create", "subject": "Open a topic", "assignee": "member-agent", "contract": contract,
		})
	}
	if r := run(t, "feishu-main", "feishu"); !r.IsError || !strings.Contains(r.ForLLM, "create_forum_topic") {
		t.Fatalf("feishu task needing a telegram-only tool must be rejected, got %q", r.ForLLM)
	}
	if r := run(t, "tg-main", "telegram"); r.IsError {
		t.Fatalf("telegram task must pass, got %q", r.ForLLM)
	}
}
