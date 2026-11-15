package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestBuildTeamMD_TaskContractGuidance(t *testing.T) {
	leadID, memberID := uuid.New(), uuid.New()
	members := []store.TeamMemberData{
		{AgentID: leadID, Role: store.TeamRoleLead, AgentKey: "lead"},
		{AgentID: memberID, Role: store.TeamRoleMember, AgentKey: "member"},
	}
	enabled := &store.TeamData{Name: "t", Settings: json.RawMessage(`{"task_contract":{"enabled":true}}`)}
	disabled := &store.TeamData{Name: "t"}

	lead := buildTeamMD(enabled, members, leadID, nil)
	for _, want := range []string{"## Task Contract (required)", "contract={", `"acceptance":`} {
		if !strings.Contains(lead, want) {
			t.Errorf("lead TEAM.md missing %q", want)
		}
	}
	member := buildTeamMD(enabled, members, memberID, nil)
	if !strings.Contains(member, `"Task Contract" block, it is the authoritative definition`) {
		t.Error("member TEAM.md must say the contract block is authoritative")
	}
	if strings.Contains(member, "## Task Contract (required)") {
		t.Error("lead-only contract guidance leaked to member")
	}
	// Team bật contract: member phải gọi complete kèm criteria, không còn câu
	// "hoàn thành là tự động" chung chung.
	if !strings.Contains(member, "criteria=[") || !strings.Contains(member, "waits for the lead's review") {
		t.Error("member TEAM.md must require complete with criteria")
	}
	if strings.Contains(member, "- Task completion is automatic when your run finishes") {
		t.Error("member TEAM.md must not say completion is automatic for contract tasks")
	}
	if !strings.Contains(lead, "waits for your review in `in_review`") {
		t.Error("lead TEAM.md must explain the review of unmet criteria")
	}
	if !strings.Contains(buildTeamMD(disabled, members, memberID, nil), "- Task completion is automatic when your run finishes") {
		t.Error("disabled team keeps automatic completion guidance")
	}

	for _, md := range []string{buildTeamMD(disabled, members, leadID, nil), buildTeamMD(disabled, members, memberID, nil)} {
		if strings.Contains(md, "Task Contract") {
			t.Error("disabled team must not mention task contracts")
		}
	}
}
