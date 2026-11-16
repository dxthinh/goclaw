//go:build integration

package integration

import (
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

// Chỉ ReopenCompletedTask mở lại task completed; ResetTaskStatus (dùng chung
// cho retry trên dashboard) vẫn từ chối, và ReopenCompletedTask từ chối task
// chưa completed.
func TestTeamTask_ReopenCompletedTask(t *testing.T) {
	db := testDB(t)
	pg.InitSqlx(db)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ts := pg.NewPGTeamStore(db)
	teamID, memberID := seedTeam(t, db, tenantID, agentID)

	task := makeTask(teamID, "reopen me", store.TeamTaskStatusPending)
	if err := ts.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := ts.ClaimTask(ctx, task.ID, memberID, teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if err := ts.ReopenCompletedTask(ctx, task.ID, teamID); err == nil {
		t.Fatal("ReopenCompletedTask on in_progress should fail")
	}
	if err := ts.CompleteTask(ctx, task.ID, teamID, "done"); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := ts.ResetTaskStatus(ctx, task.ID, teamID); err == nil {
		t.Fatal("ResetTaskStatus on completed should fail")
	}
	if err := ts.ReopenCompletedTask(ctx, task.ID, teamID); err != nil {
		t.Fatalf("ReopenCompletedTask: %v", err)
	}
	if got, _ := ts.GetTask(ctx, task.ID); got.Status != store.TeamTaskStatusPending {
		t.Fatalf("status after reopen = %q, want pending", got.Status)
	}
}
