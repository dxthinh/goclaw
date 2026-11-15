//go:build sqlite || sqliteonly

package sqlitestore

import (
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Clarify: in_progress → awaiting_input nhả khóa; ResetTaskStatus đưa về
// pending để dispatch lại; task chờ không bị recover hay đánh stale.
func TestTeamTaskSQLite_AwaitTaskInputLifecycle(t *testing.T) {
	f := newDepFixture(t)
	task := f.create(t, "needs answer")
	if err := f.teams.ClaimTask(f.ctx, task.ID, f.memberID, f.teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	future := time.Now().Add(-time.Minute)
	if err := f.teams.SetTaskFollowup(f.ctx, task.ID, f.teamID, future, 3, "waiting", "telegram", "chat-1"); err != nil {
		t.Fatalf("SetTaskFollowup: %v", err)
	}
	if err := f.teams.AwaitTaskInput(f.ctx, task.ID, f.teamID); err != nil {
		t.Fatalf("AwaitTaskInput: %v", err)
	}
	got, err := f.teams.GetTask(f.ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != store.TeamTaskStatusAwaitingInput || got.LockExpiresAt != nil {
		t.Fatalf("status=%q lock=%v, want awaiting_input without lock", got.Status, got.LockExpiresAt)
	}
	// Lần thứ hai: task không còn in_progress.
	if err := f.teams.AwaitTaskInput(f.ctx, task.ID, f.teamID); err == nil {
		t.Error("AwaitTaskInput twice should fail")
	}

	// Nhắc định kỳ vẫn tới hạn với task awaiting_input.
	due, err := f.teams.ListAllFollowupDueTasks(f.ctx)
	if err != nil {
		t.Fatalf("ListAllFollowupDueTasks: %v", err)
	}
	if !containsTask(due, task.ID) {
		t.Error("awaiting_input task with due follow-up not listed")
	}
	// Ticker: không recover, không đánh stale dù vượt mọi ngưỡng.
	recovered, _ := f.teams.ForceRecoverAllTasks(f.ctx)
	stale, _ := f.teams.MarkAllStaleTasks(f.ctx, time.Now().Add(time.Hour))
	staleReview, _ := f.teams.MarkInReviewStaleTasks(f.ctx, time.Now().Add(time.Hour))
	for _, list := range [][]store.RecoveredTaskInfo{recovered, stale, staleReview} {
		for _, r := range list {
			if r.ID == task.ID {
				t.Fatalf("ticker touched awaiting_input task")
			}
		}
	}
	if got, _ := f.teams.GetTask(f.ctx, task.ID); got.Status != store.TeamTaskStatusAwaitingInput {
		t.Fatalf("status after ticker = %q, want awaiting_input", got.Status)
	}

	if err := f.teams.ResetTaskStatus(f.ctx, task.ID, f.teamID); err != nil {
		t.Fatalf("ResetTaskStatus from awaiting_input: %v", err)
	}
	if got, _ := f.teams.GetTask(f.ctx, task.ID); got.Status != store.TeamTaskStatusPending {
		t.Fatalf("status after reset = %q, want pending", got.Status)
	}
}

// Task chờ duyệt checkpoint không bị ticker đánh stale; task in_review cũ
// (không có checkpoint_pending) vẫn bị như trước.
func TestTeamTaskSQLite_CheckpointPendingNotStale(t *testing.T) {
	f := newDepFixture(t)
	waiting := f.create(t, "waiting for user")
	legacy := f.create(t, "legacy review")
	for _, task := range []*store.TeamTaskData{waiting, legacy} {
		if err := f.teams.ClaimTask(f.ctx, task.ID, f.memberID, f.teamID); err != nil {
			t.Fatalf("ClaimTask: %v", err)
		}
		var patch map[string]any
		if task == waiting {
			patch = map[string]any{"checkpoint_pending": "user"}
		}
		if err := f.teams.SubmitTaskResultForReview(f.ctx, task.ID, f.teamID, "draft", patch); err != nil {
			t.Fatalf("SubmitTaskResultForReview: %v", err)
		}
	}

	got, _ := f.teams.GetTask(f.ctx, legacy.ID)
	if got.Status != store.TeamTaskStatusInReview || got.Result == nil || *got.Result != "draft" {
		t.Fatalf("submitted task = %q %v, want in_review with result", got.Status, got.Result)
	}

	if _, err := f.teams.MarkInReviewStaleTasks(f.ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("MarkInReviewStaleTasks: %v", err)
	}
	if got, _ := f.teams.GetTask(f.ctx, waiting.ID); got.Status != store.TeamTaskStatusInReview {
		t.Errorf("checkpoint task status = %q, want in_review", got.Status)
	}
	if got, _ := f.teams.GetTask(f.ctx, legacy.ID); got.Status != store.TeamTaskStatusStale {
		t.Errorf("legacy in_review status = %q, want stale", got.Status)
	}
}

// SubmitTaskResultForReview ghi trạng thái, result và patch metadata cùng lúc:
// patch được trộn vào metadata sẵn có; task không ở in_progress thì không phần
// nào được ghi.
func TestTeamTaskSQLite_SubmitForReviewWritesMetadataAtomically(t *testing.T) {
	f := newDepFixture(t)
	task := f.create(t, "needs approval")
	patch := map[string]any{"checkpoint_pending": "user"}

	// Task còn pending: lỗi, metadata giữ nguyên.
	if err := f.teams.SubmitTaskResultForReview(f.ctx, task.ID, f.teamID, "draft", patch); err == nil {
		t.Fatal("submit from pending should fail")
	}
	got, _ := f.teams.GetTask(f.ctx, task.ID)
	if got.Status != store.TeamTaskStatusPending || got.Metadata["checkpoint_pending"] != nil || got.Result != nil {
		t.Fatalf("failed submit wrote state: status=%q meta=%v result=%v", got.Status, got.Metadata, got.Result)
	}

	if err := f.teams.ClaimTask(f.ctx, task.ID, f.memberID, f.teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if err := f.teams.SubmitTaskResultForReview(f.ctx, task.ID, f.teamID, "draft", patch); err != nil {
		t.Fatalf("SubmitTaskResultForReview: %v", err)
	}
	got, _ = f.teams.GetTask(f.ctx, task.ID)
	if got.Status != store.TeamTaskStatusInReview || got.Result == nil || *got.Result != "draft" ||
		got.Metadata["checkpoint_pending"] != "user" || got.Metadata["source"] != "test" {
		t.Fatalf("submit: status=%q result=%v meta=%v, want in_review with merged metadata", got.Status, got.Result, got.Metadata)
	}
}

// RejectTask huỷ task đang chờ user duyệt và xoá checkpoint_pending trong cùng
// thao tác; metadata khác giữ nguyên.
func TestTeamTaskSQLite_RejectClearsCheckpointPending(t *testing.T) {
	f := newDepFixture(t)
	task := f.create(t, "waiting for user")
	if err := f.teams.ClaimTask(f.ctx, task.ID, f.memberID, f.teamID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if err := f.teams.SubmitTaskResultForReview(f.ctx, task.ID, f.teamID, "draft", map[string]any{"checkpoint_pending": "user"}); err != nil {
		t.Fatalf("SubmitTaskResultForReview: %v", err)
	}
	if err := f.teams.RejectTask(f.ctx, task.ID, f.teamID, "not needed"); err != nil {
		t.Fatalf("RejectTask: %v", err)
	}
	got, _ := f.teams.GetTask(f.ctx, task.ID)
	if _, has := got.Metadata["checkpoint_pending"]; got.Status != store.TeamTaskStatusCancelled || has || got.Metadata["source"] != "test" {
		t.Fatalf("after reject: status=%q meta=%v, want cancelled without checkpoint_pending", got.Status, got.Metadata)
	}
}

// GetTaskByNumberInChat chỉ trả task đúng số và đúng chat gốc.
func TestTeamTaskSQLite_GetTaskByNumberInChat(t *testing.T) {
	f := newDepFixture(t)
	a := f.create(t, "chat one")
	b := f.create(t, "chat two")
	mustExec(t, f.db, `UPDATE team_tasks SET channel = 'tg', chat_id = '-100' WHERE id = ?`, a.ID)
	mustExec(t, f.db, `UPDATE team_tasks SET channel = 'tg', chat_id = '-200' WHERE id = ?`, b.ID)

	got, err := f.teams.GetTaskByNumberInChat(f.ctx, f.teamID, a.TaskNumber, "tg", "-100")
	if err != nil || got.ID != a.ID {
		t.Fatalf("lookup = %v, %v; want task A", got, err)
	}
	if _, err := f.teams.GetTaskByNumberInChat(f.ctx, f.teamID, b.TaskNumber, "tg", "-100"); err != store.ErrTaskNotFound {
		t.Errorf("task of another chat: err = %v, want ErrTaskNotFound", err)
	}
	if _, err := f.teams.GetTaskByNumberInChat(f.ctx, f.teamID, a.TaskNumber, "other", "-100"); err != store.ErrTaskNotFound {
		t.Errorf("other channel: err = %v, want ErrTaskNotFound", err)
	}
}

func containsTask(tasks []store.TeamTaskData, id interface{ String() string }) bool {
	for _, t := range tasks {
		if t.ID.String() == id.String() {
			return true
		}
	}
	return false
}
