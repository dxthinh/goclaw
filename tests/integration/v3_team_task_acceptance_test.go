//go:build integration

package integration

import (
	"encoding/json"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// Task có contract mà tiêu chí chưa đạt: SubmitResultForLeadReview ghi
// in_review, result, checkpoint_pending = "lead" và acceptance_report trong một
// câu UPDATE; contract sẵn có giữ nguyên, báo cáo đọc lại đúng hình dạng.
func TestTeamTask_LeadReviewWritesAcceptanceReport(t *testing.T) {
	db := testDB(t)
	pg.InitSqlx(db)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ts := pg.NewPGTeamStore(db)
	teamID, memberID := seedTeam(t, db, tenantID, agentID)

	contract, err := tools.NewTaskContractFromInput(map[string]any{
		"objective": "Brief the board", "deliverables": []any{"q3.md"}, "acceptance": []any{"All regions", "Cites sources"},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := makeTask(teamID, "q3 brief", store.TeamTaskStatusPending)
	task.Metadata = map[string]any{tools.TaskMetaContract: contract.ToMetadata()}
	if err := ts.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := ts.ClaimTask(ctx, task.ID, memberID, teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	team, err := ts.GetTeam(ctx, teamID)
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	criteria, err := tools.ParseAcceptanceCriteria([]any{
		map[string]any{"index": float64(1), "met": true, "evidence": "4 regions in q3.md"},
		map[string]any{"index": float64(2), "met": false, "evidence": "2 numbers unsourced"},
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(criteria)
	var report []any
	_ = json.Unmarshal(b, &report)

	loaded, _ := ts.GetTask(ctx, task.ID)
	if err := tools.SubmitResultForLeadReview(ctx, ts, team, loaded, "draft", map[string]any{tools.TaskMetaAcceptanceReport: report}); err != nil {
		t.Fatalf("SubmitResultForLeadReview: %v", err)
	}
	got, _ := ts.GetTask(ctx, task.ID)
	if got.Status != store.TeamTaskStatusInReview || got.Result == nil || *got.Result != "draft" ||
		tools.TaskCheckpointPending(got) != tools.CheckpointPendingLead {
		t.Fatalf("status=%q result=%v pending=%q, want in_review waiting for lead", got.Status, got.Result, tools.TaskCheckpointPending(got))
	}
	if c := tools.TaskContractFromTask(got); c == nil || len(c.Acceptance) != 2 {
		t.Fatalf("contract lost after submit: %v", got.Metadata)
	}
	rep, _ := got.Metadata[tools.TaskMetaAcceptanceReport].([]any)
	if len(rep) != 2 {
		t.Fatalf("acceptance_report = %v", got.Metadata[tools.TaskMetaAcceptanceReport])
	}
	if first, _ := rep[1].(map[string]any); first["met"] != false || first["evidence"] != "2 numbers unsourced" {
		t.Errorf("acceptance_report[1] = %v", rep[1])
	}

	// Lần nộp thứ hai (task đã in_review) bị từ chối, không ghi đè.
	if err := tools.SubmitResultForLeadReview(ctx, ts, team, got, "other", nil); err == nil {
		t.Error("second submit from in_review should fail")
	}
}
