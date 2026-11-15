//go:build integration

package integration

import (
	"errors"
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
		var patch map[string]any
		if task == waiting {
			patch = map[string]any{"checkpoint_pending": "user"}
		}
		if err := ts.SubmitTaskResultForReview(ctx, task.ID, teamID, "draft", patch); err != nil {
			t.Fatalf("SubmitTaskResultForReview: %v", err)
		}
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

// SubmitTaskResultForReview ghi trạng thái, result và patch metadata trong một
// câu UPDATE: patch trộn vào metadata sẵn có; task không ở in_progress (hoặc
// sai tenant) thì không phần nào được ghi.
func TestTeamTask_SubmitForReviewWritesMetadataAtomically(t *testing.T) {
	db := testDB(t)
	pg.InitSqlx(db)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ts := pg.NewPGTeamStore(db)
	teamID, memberID := seedTeam(t, db, tenantID, agentID)

	task := makeTask(teamID, "needs approval", store.TeamTaskStatusPending)
	task.Metadata = map[string]any{"source": "test"}
	if err := ts.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	patch := map[string]any{"checkpoint_pending": "user"}

	if err := ts.SubmitTaskResultForReview(ctx, task.ID, teamID, "draft", patch); err == nil {
		t.Fatal("submit from pending should fail")
	}
	if err := ts.ClaimTask(ctx, task.ID, memberID, teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	otherTenant, _ := seedTenantAgent(t, db)
	if err := ts.SubmitTaskResultForReview(tenantCtx(otherTenant), task.ID, teamID, "draft", patch); err == nil {
		t.Fatal("submit from another tenant should fail")
	}
	got, _ := ts.GetTask(ctx, task.ID)
	if got.Status != store.TeamTaskStatusInProgress || got.Metadata["checkpoint_pending"] != nil || got.Result != nil {
		t.Fatalf("failed submit wrote state: status=%q meta=%v result=%v", got.Status, got.Metadata, got.Result)
	}

	if err := ts.SubmitTaskResultForReview(ctx, task.ID, teamID, "draft", patch); err != nil {
		t.Fatalf("SubmitTaskResultForReview: %v", err)
	}
	got, _ = ts.GetTask(ctx, task.ID)
	if got.Status != store.TeamTaskStatusInReview || got.Result == nil || *got.Result != "draft" ||
		got.Metadata["checkpoint_pending"] != "user" || got.Metadata["source"] != "test" {
		t.Fatalf("submit: status=%q result=%v meta=%v, want in_review with merged metadata", got.Status, got.Result, got.Metadata)
	}
}

// RejectTask (dashboard huỷ task) xoá checkpoint_pending trong cùng câu UPDATE
// huỷ task; metadata khác giữ nguyên.
func TestTeamTask_RejectClearsCheckpointPending(t *testing.T) {
	db := testDB(t)
	pg.InitSqlx(db)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ts := pg.NewPGTeamStore(db)
	teamID, memberID := seedTeam(t, db, tenantID, agentID)

	task := makeTask(teamID, "waiting for user", store.TeamTaskStatusPending)
	task.Metadata = map[string]any{"source": "test"}
	if err := ts.CreateTask(ctx, task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := ts.ClaimTask(ctx, task.ID, memberID, teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if err := ts.SubmitTaskResultForReview(ctx, task.ID, teamID, "draft", map[string]any{"checkpoint_pending": "user"}); err != nil {
		t.Fatalf("SubmitTaskResultForReview: %v", err)
	}
	if err := ts.RejectTask(ctx, task.ID, teamID, "not needed"); err != nil {
		t.Fatalf("RejectTask: %v", err)
	}
	got, _ := ts.GetTask(ctx, task.ID)
	if _, has := got.Metadata["checkpoint_pending"]; got.Status != store.TeamTaskStatusCancelled || has || got.Metadata["source"] != "test" {
		t.Fatalf("after reject: status=%q meta=%v, want cancelled without checkpoint_pending", got.Status, got.Metadata)
	}
}

// GetTaskByNumberInChat chỉ trả task đúng số và đúng chat gốc, kể cả khi
// team có nhiều task hơn một trang danh sách.
func TestTeamTask_GetTaskByNumberInChat(t *testing.T) {
	db := testDB(t)
	pg.InitSqlx(db)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ts := pg.NewPGTeamStore(db)
	teamID, _ := seedTeam(t, db, tenantID, agentID)

	var last *store.TeamTaskData
	for i := 0; i < 205; i++ {
		task := makeTask(teamID, "bulk", store.TeamTaskStatusPending)
		task.Channel, task.ChatID = "tg", "-100"
		if err := ts.CreateTask(ctx, task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
		last = task
	}
	got, err := ts.GetTaskByNumberInChat(ctx, teamID, last.TaskNumber, "tg", "-100")
	if err != nil || got.ID != last.ID {
		t.Fatalf("lookup = %v, %v; want last task", got, err)
	}
	if _, err := ts.GetTaskByNumberInChat(ctx, teamID, last.TaskNumber, "tg", "-200"); !errors.Is(err, store.ErrTaskNotFound) {
		t.Errorf("other chat: err = %v, want ErrTaskNotFound", err)
	}
}
