//go:build integration

package integration

import (
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

// RedispatchReviewedTask chỉ nhận task in_review; chuyển thẳng sang in_progress
// cho owner, xoá result và checkpoint_pending, thay nguyên khối khóa trong
// metadataSet và giữ các khóa khác.
func TestTeamTask_RedispatchReviewedTask(t *testing.T) {
	db := testDB(t)
	pg.InitSqlx(db)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ts := pg.NewPGTeamStore(db)
	teamID, memberID := seedTeam(t, db, tenantID, agentID)

	task := makeTask(teamID, "rework", store.TeamTaskStatusPending)
	task.Metadata = map[string]any{"source": "test", "contract": map[string]any{"version": 1, "old": true}}
	if err := ts.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := ts.ClaimTask(ctx, task.ID, memberID, teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	set := map[string]any{"contract": map[string]any{"version": 2}}
	if err := ts.RedispatchReviewedTask(ctx, task.ID, teamID, memberID, set); err == nil {
		t.Fatal("redispatch from in_progress should fail")
	}
	if err := ts.SubmitTaskResultForReview(ctx, task.ID, teamID, "draft", map[string]any{"checkpoint_pending": "lead"}); err != nil {
		t.Fatalf("SubmitTaskResultForReview: %v", err)
	}
	if err := ts.RedispatchReviewedTask(ctx, task.ID, teamID, memberID, set); err != nil {
		t.Fatalf("RedispatchReviewedTask: %v", err)
	}
	got, _ := ts.GetTask(ctx, task.ID)
	contract, _ := got.Metadata["contract"].(map[string]any)
	_, hasMarker := got.Metadata["checkpoint_pending"]
	if got.Status != store.TeamTaskStatusInProgress || got.LockExpiresAt == nil || got.Result != nil ||
		got.OwnerAgentID == nil || *got.OwnerAgentID != memberID || hasMarker || got.Metadata["source"] != "test" ||
		len(contract) != 1 || contract["version"] != float64(2) {
		t.Fatalf("after redispatch: status=%q lock=%v result=%v meta=%v", got.Status, got.LockExpiresAt, got.Result, got.Metadata)
	}
}
