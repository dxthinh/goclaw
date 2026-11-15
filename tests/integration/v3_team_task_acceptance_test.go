//go:build integration

package integration

import (
	"encoding/json"
	"errors"
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

// TransitionTaskCheckpointPending (PG): đổi marker lead → user bằng một câu
// UPDATE có điều kiện; sai marker, sai tenant hoặc task đã completed thì báo
// xung đột và không ghi gì; metadata khác (contract) giữ nguyên.
func TestTeamTask_TransitionCheckpointPending(t *testing.T) {
	db := testDB(t)
	pg.InitSqlx(db)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ts := pg.NewPGTeamStore(db)
	teamID, memberID := seedTeam(t, db, tenantID, agentID)

	task := makeTask(teamID, "lead review", store.TeamTaskStatusPending)
	task.Metadata = map[string]any{"source": "test"}
	if err := ts.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := ts.ClaimTask(ctx, task.ID, memberID, teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if err := ts.SubmitTaskResultForReview(ctx, task.ID, teamID, "draft", map[string]any{"checkpoint_pending": "lead"}); err != nil {
		t.Fatalf("SubmitTaskResultForReview: %v", err)
	}

	if err := ts.TransitionTaskCheckpointPending(ctx, task.ID, teamID, "user", "lead"); !errors.Is(err, store.ErrTaskCheckpointConflict) {
		t.Fatalf("wrong from marker: err = %v, want conflict", err)
	}
	otherTenant, _ := seedTenantAgent(t, db)
	if err := ts.TransitionTaskCheckpointPending(tenantCtx(otherTenant), task.ID, teamID, "lead", "user"); !errors.Is(err, store.ErrTaskCheckpointConflict) {
		t.Fatalf("other tenant: err = %v, want conflict", err)
	}
	if err := ts.TransitionTaskCheckpointPending(ctx, task.ID, teamID, "lead", "user"); err != nil {
		t.Fatalf("TransitionTaskCheckpointPending: %v", err)
	}
	got, _ := ts.GetTask(ctx, task.ID)
	if got.Status != store.TeamTaskStatusInReview || got.Metadata["checkpoint_pending"] != "user" || got.Metadata["source"] != "test" {
		t.Fatalf("after transition: status=%q meta=%v", got.Status, got.Metadata)
	}

	if err := ts.ApproveTask(ctx, task.ID, teamID, ""); err != nil {
		t.Fatalf("ApproveTask: %v", err)
	}
	if err := ts.TransitionTaskCheckpointPending(ctx, task.ID, teamID, "user", "lead"); !errors.Is(err, store.ErrTaskCheckpointConflict) {
		t.Fatalf("completed task: err = %v, want conflict", err)
	}
}

// CompleteTaskWithMetadata (PG): báo cáo tiêu chí ghi cùng câu hoàn thành;
// CompleteTask không chạm metadata; task không in_progress thì lỗi.
func TestTeamTask_CompleteTaskWithMetadata(t *testing.T) {
	db := testDB(t)
	pg.InitSqlx(db)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ts := pg.NewPGTeamStore(db)
	teamID, memberID := seedTeam(t, db, tenantID, agentID)

	withReport := makeTask(teamID, "with report", store.TeamTaskStatusPending)
	plain := makeTask(teamID, "plain", store.TeamTaskStatusPending)
	for _, task := range []*store.TeamTaskData{withReport, plain} {
		task.Metadata = map[string]any{"source": "test"}
		if err := ts.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		if err := ts.ClaimTask(ctx, task.ID, memberID, teamID); err != nil {
			t.Fatalf("ClaimTask: %v", err)
		}
	}
	report := []any{map[string]any{"index": 1, "met": true, "evidence": "ok"}}
	if err := ts.CompleteTaskWithMetadata(ctx, withReport.ID, teamID, "done", map[string]any{tools.TaskMetaAcceptanceReport: report}); err != nil {
		t.Fatalf("CompleteTaskWithMetadata: %v", err)
	}
	got, _ := ts.GetTask(ctx, withReport.ID)
	rep, _ := got.Metadata[tools.TaskMetaAcceptanceReport].([]any)
	if got.Status != store.TeamTaskStatusCompleted || len(rep) != 1 || got.Metadata["source"] != "test" {
		t.Fatalf("with report: status=%q meta=%v", got.Status, got.Metadata)
	}
	if err := ts.CompleteTask(ctx, plain.ID, teamID, "done"); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	got, _ = ts.GetTask(ctx, plain.ID)
	if got.Status != store.TeamTaskStatusCompleted || len(got.Metadata) != 1 {
		t.Fatalf("plain: status=%q meta=%v", got.Status, got.Metadata)
	}
	if err := ts.CompleteTaskWithMetadata(ctx, plain.ID, teamID, "again", map[string]any{"x": 1}); err == nil {
		t.Fatal("completing a completed task should fail")
	}
}
