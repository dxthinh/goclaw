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

	for _, md := range []string{buildTeamMD(disabled, members, leadID, nil), buildTeamMD(disabled, members, memberID, nil)} {
		if strings.Contains(md, "Task Contract") {
			t.Error("disabled team must not mention task contracts")
		}
	}
}
