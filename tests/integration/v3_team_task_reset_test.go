//go:build integration

package integration

import (
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

// ResetTaskStatus mở lại được task completed (tool retry của lead), nhưng vẫn
// từ chối task đang chạy.
func TestTeamTask_ResetTaskStatusReopensCompleted(t *testing.T) {
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
	if err := ts.ResetTaskStatus(ctx, task.ID, teamID); err == nil {
		t.Fatal("ResetTaskStatus on in_progress should fail")
	}
	if err := ts.CompleteTask(ctx, task.ID, teamID, "done"); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := ts.ResetTaskStatus(ctx, task.ID, teamID); err != nil {
		t.Fatalf("ResetTaskStatus from completed: %v", err)
	}
	if got, _ := ts.GetTask(ctx, task.ID); got.Status != store.TeamTaskStatusPending {
		t.Fatalf("status after reset = %q, want pending", got.Status)
	}
}
