//go:build integration

package integration

import (
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

// Clarify: in_progress → awaiting_input nhả khóa; nhắc định kỳ vẫn tới hạn;
// ticker không recover hay đánh stale; ResetTaskStatus đưa về pending.
func TestTeamTask_AwaitTaskInputLifecycle(t *testing.T) {
	db := testDB(t)
	pg.InitSqlx(db)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ts := pg.NewPGTeamStore(db)
	teamID, memberID := seedTeam(t, db, tenantID, agentID)

	task := makeTask(teamID, "needs answer", store.TeamTaskStatusPending)
	if err := ts.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := ts.ClaimTask(ctx, task.ID, memberID, teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if err := ts.SetTaskFollowup(ctx, task.ID, teamID, time.Now().Add(-time.Minute), 3, "waiting", "telegram", "chat-1"); err != nil {
		t.Fatalf("SetTaskFollowup: %v", err)
	}
	if err := ts.AwaitTaskInput(ctx, task.ID, teamID); err != nil {
		t.Fatalf("AwaitTaskInput: %v", err)
	}
	got, err := ts.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != store.TeamTaskStatusAwaitingInput || got.LockExpiresAt != nil {
		t.Fatalf("status=%q lock=%v, want awaiting_input without lock", got.Status, got.LockExpiresAt)
	}
	if err := ts.AwaitTaskInput(ctx, task.ID, teamID); err == nil {
		t.Error("AwaitTaskInput twice should fail")
	}

	due, err := ts.ListAllFollowupDueTasks(ctx)
	if err != nil {
		t.Fatalf("ListAllFollowupDueTasks: %v", err)
	}
	found := false
	for _, d := range due {
		found = found || d.ID == task.ID
	}
	if !found {
		t.Error("awaiting_input task with due follow-up not listed")
	}

	recovered, _ := ts.ForceRecoverAllTasks(ctx)
	stale, _ := ts.MarkAllStaleTasks(ctx, time.Now().Add(time.Hour))
	staleReview, _ := ts.MarkInReviewStaleTasks(ctx, time.Now().Add(time.Hour))
	for _, list := range [][]store.RecoveredTaskInfo{recovered, stale, staleReview} {
		for _, r := range list {
			if r.ID == task.ID {
				t.Fatal("ticker touched awaiting_input task")
			}
		}
	}
	if busy, _ := ts.HasActiveMemberTasks(ctx, teamID, agentID); !busy {
		t.Error("HasActiveMemberTasks should count awaiting_input tasks")
	}

	if err := ts.ResetTaskStatus(ctx, task.ID, teamID); err != nil {
		t.Fatalf("ResetTaskStatus from awaiting_input: %v", err)
	}
	if got, _ := ts.GetTask(ctx, task.ID); got.Status != store.TeamTaskStatusPending {
		t.Fatalf("status after reset = %q, want pending", got.Status)
	}
}

// Task chờ duyệt checkpoint không bị đánh stale; in_review cũ thì vẫn bị.
func TestTeamTask_CheckpointPendingNotStale(t *testing.T) {
	db := testDB(t)
	pg.InitSqlx(db)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ts := pg.NewPGTeamStore(db)
	teamID, memberID := seedTeam(t, db, tenantID, agentID)

	waiting := makeTask(teamID, "waiting for user", store.TeamTaskStatusPending)
	legacy := makeTask(teamID, "legacy review", store.TeamTaskStatusPending)
	for _, task := range []*store.TeamTaskData{waiting, legacy} {
		if err := ts.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		if err := ts.ClaimTask(ctx, task.ID, memberID, teamID); err != nil {
			t.Fatalf("ClaimTask: %v", err)
		}
		if err := ts.SubmitTaskResultForReview(ctx, task.ID, teamID, "draft"); err != nil {
			t.Fatalf("SubmitTaskResultForReview: %v", err)
		}
	}
	if _, err := db.Exec(`UPDATE team_tasks SET metadata = metadata || '{"checkpoint_pending":"user"}'::jsonb WHERE id = $1`, waiting.ID); err != nil {
		t.Fatalf("set checkpoint_pending: %v", err)
	}
	if got, _ := ts.GetTask(ctx, legacy.ID); got.Status != store.TeamTaskStatusInReview || got.Result == nil || *got.Result != "draft" {
		t.Fatalf("submitted task = %q %v, want in_review with result", got.Status, got.Result)
	}

	if _, err := ts.MarkInReviewStaleTasks(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("MarkInReviewStaleTasks: %v", err)
	}
	if got, _ := ts.GetTask(ctx, waiting.ID); got.Status != store.TeamTaskStatusInReview {
		t.Errorf("checkpoint task status = %q, want in_review", got.Status)
	}
	if got, _ := ts.GetTask(ctx, legacy.ID); got.Status != store.TeamTaskStatusStale {
		t.Errorf("legacy in_review status = %q, want stale", got.Status)
	}
}
