package agent

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// capabilityAgentStore chỉ cài GetByIDs, đủ cho teamMemberCapabilities.
type capabilityAgentStore struct {
	store.AgentStore
	agents []store.AgentData
}

func (s *capabilityAgentStore) GetByIDs(_ context.Context, ids []uuid.UUID) ([]store.AgentData, error) {
	var out []store.AgentData
	for _, a := range s.agents {
		if slices.Contains(ids, a.ID) {
			out = append(out, a)
		}
	}
	return out, nil
}

func newCapabilityRegistry() (*tools.Registry, *tools.PolicyEngine) {
	reg := tools.NewRegistry()
	for _, n := range []string{"web_search", "web_fetch", "read_file", "write_file", "list_files", "edit", "exec", "wait", "team_tasks", "memory_search"} {
		reg.Register(&mockTool{name: n, desc: n})
	}
	return reg, tools.NewPolicyEngine(&config.ToolsConfig{})
}

// TestCapabilityParityWithResolverPolicy giữ EffectiveTools (cổng needs) và
// policy mà resolver dựng cho agent trong team (resolver.go, AgentToolPolicy)
// cho ra cùng một tập tool.
func TestCapabilityParityWithResolverPolicy(t *testing.T) {
	reg, pe := newCapabilityRegistry()
	checker := tools.NewTeamCapabilityChecker(pe, reg)
	cases := map[string]string{
		"no config":    "",
		"deny":         `{"deny":["group:web","exec"]}`,
		"narrow allow": `{"allow":["web_search"]}`,
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			ag := &store.AgentData{AgentKey: "member", Provider: "openai"}
			if cfg != "" {
				ag.ToolsConfig = json.RawMessage(cfg)
			}
			policy := agentToolPolicyForTeam(agentToolPolicyWithWorkspace(agentToolPolicyWithMCP(ag.ParseToolsConfig(), false), true), false)
			want := map[string]bool{}
			for _, d := range pe.FilterTools(reg, ag.AgentKey, ag.Provider, policy, nil, false, false) {
				want[d.Function.Name] = true
			}
			got := checker.EffectiveTools(ag)
			if !maps.Equal(got, want) {
				t.Errorf("EffectiveTools = %v, resolver policy = %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
			}
		})
	}
}

func capabilityTeamFixture(settings string) (*store.TeamData, []store.TeamMemberData, *capabilityAgentStore) {
	leadID, webID, noWebID := uuid.New(), uuid.New(), uuid.New()
	team := &store.TeamData{Name: "t", LeadAgentID: leadID, Settings: json.RawMessage(settings)}
	members := []store.TeamMemberData{
		{AgentID: leadID, Role: store.TeamRoleLead, AgentKey: "lead"},
		{AgentID: webID, Role: store.TeamRoleMember, AgentKey: "researcher"},
		{AgentID: noWebID, Role: store.TeamRoleMember, AgentKey: "coder"},
	}
	agents := &capabilityAgentStore{agents: []store.AgentData{
		{BaseModel: store.BaseModel{ID: leadID}, AgentKey: "lead"},
		{BaseModel: store.BaseModel{ID: webID}, AgentKey: "researcher"},
		{BaseModel: store.BaseModel{ID: noWebID}, AgentKey: "coder", ToolsConfig: json.RawMessage(`{"deny":["group:web"]}`)},
	}}
	return team, members, agents
}

func TestTeamMDCapabilities(t *testing.T) {
	ctx := context.Background()
	reg, pe := newCapabilityRegistry()
	enabled := `{"task_contract":{"enabled":true}}`

	t.Run("lead of enabled team sees one line per member", func(t *testing.T) {
		team, members, agents := capabilityTeamFixture(enabled)
		caps, deps := teamMemberCapabilities(ctx, pe, reg, agents, team, members, team.LeadAgentID)
		if !slices.Equal(deps, []string{"researcher", "coder"}) {
			t.Errorf("cache deps = %v", deps)
		}
		md := buildTeamMD(team, members, team.LeadAgentID, caps)
		if !strings.Contains(md, "## Member Tool Groups") {
			t.Fatalf("missing capability section:\n%s", md)
		}
		if !strings.Contains(md, "- **researcher**: has web\n") || !strings.Contains(md, "- **coder**: has none; lacks web\n") {
			t.Errorf("unexpected capability lines:\n%s", md)
		}
		if !strings.Contains(md, "`needs`") {
			t.Error("lead contract guidance must mention needs")
		}
	})
	t.Run("member never sees the section", func(t *testing.T) {
		team, members, agents := capabilityTeamFixture(enabled)
		memberID := members[1].AgentID
		caps, deps := teamMemberCapabilities(ctx, pe, reg, agents, team, members, memberID)
		if caps != nil || deps != nil {
			t.Errorf("member must not get capabilities, got %v %v", caps, deps)
		}
		if strings.Contains(buildTeamMD(team, members, memberID, caps), "Member Tool Groups") {
			t.Error("member TEAM.md must not list capabilities")
		}
	})
	t.Run("disabled team or identical members print nothing", func(t *testing.T) {
		team, members, agents := capabilityTeamFixture(`{}`)
		if caps, _ := teamMemberCapabilities(ctx, pe, reg, agents, team, members, team.LeadAgentID); caps != nil {
			t.Errorf("disabled team must not compute capabilities, got %v", caps)
		}
		team, members, agents = capabilityTeamFixture(enabled)
		agents.agents[2].ToolsConfig = nil
		caps, _ := teamMemberCapabilities(ctx, pe, reg, agents, team, members, team.LeadAgentID)
		if caps != nil || strings.Contains(buildTeamMD(team, members, team.LeadAgentID, caps), "Member Tool Groups") {
			t.Errorf("identical members must print nothing, got %v", caps)
		}
	})
	t.Run("no policy engine skips", func(t *testing.T) {
		team, members, agents := capabilityTeamFixture(enabled)
		if caps, _ := teamMemberCapabilities(ctx, nil, reg, agents, team, members, team.LeadAgentID); caps != nil {
			t.Errorf("nil policy engine must skip, got %v", caps)
		}
	})
}

// TestRouterInvalidatesDependentLead: sửa tool của member xóa cache của lead
// vì TEAM.md của lead chứa tóm tắt năng lực của member đó.
func TestRouterInvalidatesDependentLead(t *testing.T) {
	r := NewRouter()
	now := time.Now()
	r.agents["tenant:lead"] = &agentEntry{agent: &Loop{id: "lead", cacheDependsOn: []string{"coder"}}, cachedAt: now}
	r.agents["tenant:coder"] = &agentEntry{agent: &Loop{id: "coder"}, cachedAt: now}
	r.agents["tenant:other"] = &agentEntry{agent: &Loop{id: "other"}, cachedAt: now}

	r.InvalidateAgent("coder")

	if _, ok := r.agents["tenant:lead"]; ok {
		t.Error("lead depending on coder must be invalidated")
	}
	if _, ok := r.agents["tenant:coder"]; ok {
		t.Error("coder itself must be invalidated")
	}
	if _, ok := r.agents["tenant:other"]; !ok {
		t.Error("unrelated agent must stay cached")
	}
}
