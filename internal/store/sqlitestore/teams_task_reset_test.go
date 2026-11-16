//go:build sqlite || sqliteonly

package sqlitestore

import (
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// ResetTaskStatus mở lại được task completed (tool retry của lead), nhưng vẫn
// từ chối task đang chạy.
func TestTeamTaskSQLite_ResetTaskStatusReopensCompleted(t *testing.T) {
	f := newDepFixture(t)
	task := f.create(t, "reopen me")
	if err := f.teams.ClaimTask(f.ctx, task.ID, f.memberID, f.teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if err := f.teams.ResetTaskStatus(f.ctx, task.ID, f.teamID); err == nil {
		t.Fatal("ResetTaskStatus on in_progress should fail")
	}
	if err := f.teams.CompleteTask(f.ctx, task.ID, f.teamID, "done"); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := f.teams.ResetTaskStatus(f.ctx, task.ID, f.teamID); err != nil {
		t.Fatalf("ResetTaskStatus from completed: %v", err)
	}
	if got, _ := f.teams.GetTask(f.ctx, task.ID); got.Status != store.TeamTaskStatusPending {
		t.Fatalf("status after reset = %q, want pending", got.Status)
	}
}
