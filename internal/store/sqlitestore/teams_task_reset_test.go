//go:build sqlite || sqliteonly

package sqlitestore

import (
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Chỉ ReopenCompletedTask mở lại task completed; ResetTaskStatus (dùng chung
// cho retry trên dashboard) vẫn từ chối, và ReopenCompletedTask từ chối task
// chưa completed.
func TestTeamTaskSQLite_ReopenCompletedTask(t *testing.T) {
	f := newDepFixture(t)
	task := f.create(t, "reopen me")
	if err := f.teams.ClaimTask(f.ctx, task.ID, f.memberID, f.teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if err := f.teams.ReopenCompletedTask(f.ctx, task.ID, f.teamID); err == nil {
		t.Fatal("ReopenCompletedTask on in_progress should fail")
	}
	if err := f.teams.CompleteTask(f.ctx, task.ID, f.teamID, "done"); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if err := f.teams.ResetTaskStatus(f.ctx, task.ID, f.teamID); err == nil {
		t.Fatal("ResetTaskStatus on completed should fail")
	}
	if err := f.teams.ReopenCompletedTask(f.ctx, task.ID, f.teamID); err != nil {
		t.Fatalf("ReopenCompletedTask: %v", err)
	}
	if got, _ := f.teams.GetTask(f.ctx, task.ID); got.Status != store.TeamTaskStatusPending {
		t.Fatalf("status after reopen = %q, want pending", got.Status)
	}
}
