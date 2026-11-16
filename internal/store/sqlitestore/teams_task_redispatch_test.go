//go:build sqlite || sqliteonly

package sqlitestore

import (
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// RedispatchReviewedTask chỉ nhận task in_review; chuyển thẳng sang in_progress
// cho owner, xoá result và checkpoint_pending, thay nguyên khối khóa trong
// metadataSet (không trộn đệ quy) và giữ các khóa khác.
func TestTeamTaskSQLite_RedispatchReviewedTask(t *testing.T) {
	f := newDepFixture(t)
	task := &store.TeamTaskData{TeamID: f.teamID, Subject: "rework", Status: store.TeamTaskStatusPending,
		Metadata: map[string]any{"source": "test", "contract": map[string]any{"version": 1, "old": true}}}
	if err := f.teams.CreateTask(f.ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := f.teams.ClaimTask(f.ctx, task.ID, f.memberID, f.teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	set := map[string]any{"contract": map[string]any{"version": 2}}
	if err := f.teams.RedispatchReviewedTask(f.ctx, task.ID, f.teamID, f.memberID, set); err == nil {
		t.Fatal("redispatch from in_progress should fail")
	}
	if err := f.teams.SubmitTaskResultForReview(f.ctx, task.ID, f.teamID, "draft", map[string]any{"checkpoint_pending": "lead"}); err != nil {
		t.Fatalf("SubmitTaskResultForReview: %v", err)
	}
	if err := f.teams.RedispatchReviewedTask(f.ctx, task.ID, f.teamID, f.memberID, set); err != nil {
		t.Fatalf("RedispatchReviewedTask: %v", err)
	}
	got, _ := f.teams.GetTask(f.ctx, task.ID)
	contract, _ := got.Metadata["contract"].(map[string]any)
	_, hasMarker := got.Metadata["checkpoint_pending"]
	if got.Status != store.TeamTaskStatusInProgress || got.LockExpiresAt == nil || got.Result != nil ||
		got.OwnerAgentID == nil || *got.OwnerAgentID != f.memberID || hasMarker || got.Metadata["source"] != "test" ||
		len(contract) != 1 || contract["version"] != float64(2) {
		t.Fatalf("after redispatch: status=%q lock=%v result=%v meta=%v", got.Status, got.LockExpiresAt, got.Result, got.Metadata)
	}
}
