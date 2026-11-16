package tools

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// reject trên task chưa nộp kết quả bị từ chối trước mọi lần ghi: không comment,
// không amendment, không đổi trạng thái, không dispatch.
func TestReject_NotInReviewRefusedWithoutSideEffects(t *testing.T) {
	for _, status := range []string{store.TeamTaskStatusInProgress, store.TeamTaskStatusAwaitingInput} {
		mb, tool, leadID, _, ctx := newTestTeamSetup()
		taskID := makeFeedbackTask(t, mb, status, true, "")

		res := tool.Execute(store.WithAgentID(ctx, leadID), map[string]any{"action": "reject", "task_id": taskID.String(), "text": "redo"})
		if !res.IsError || !strings.Contains(res.ForLLM, "in_review") {
			t.Fatalf("%s: want refusal naming in_review, got %q", status, res.ForLLM)
		}
		if got := readTask(mb, taskID).Status; got != status {
			t.Errorf("%s: status = %q, want unchanged", status, got)
		}
		if c := taskContract(t, mb, taskID); len(c.Amendments) != 0 {
			t.Errorf("%s: amendments = %+v, want none", status, c.Amendments)
		}
		if comments, _ := mb.taskStore.ListTaskComments(ctx, taskID); len(comments) != 0 {
			t.Errorf("%s: comments = %d, want none", status, len(comments))
		}
		if len(mb.dispatches) != 0 {
			t.Errorf("%s: dispatches = %d, want none", status, len(mb.dispatches))
		}
	}
}

// retry từ chối task completed với lý do rõ ràng, không đổi trạng thái hay
// dispatch: task phụ thuộc đã được mở khoá lúc task hoàn thành.
func TestRetry_CompletedTaskRefused(t *testing.T) {
	mb, tool, leadID, memberID, ctx := newTestTeamSetup()
	taskID := makeTask(mb, testTeamID, memberID, store.TeamTaskStatusCompleted)

	lCtx := WithTaskActionFlags(store.WithAgentID(ctx, leadID), &TaskActionFlags{})
	res := tool.Execute(lCtx, map[string]any{"action": "retry", "task_id": taskID.String()})
	if !res.IsError || !strings.Contains(res.ForLLM, "new task") {
		t.Fatalf("want refusal suggesting a new task, got %q", res.ForLLM)
	}
	if got := readTask(mb, taskID).Status; got != store.TeamTaskStatusCompleted {
		t.Errorf("status = %q, want completed", got)
	}
	if len(mb.dispatches) != 0 {
		t.Errorf("dispatches = %d, want 0", len(mb.dispatches))
	}
}

// Task được duyệt ngay sau lần đọc của reject: phép chuyển trạng thái thất bại
// nên không có comment, amendment hay dispatch nào bám vào task đã completed.
func TestReject_ApprovedConcurrentlyLeavesNoSideEffects(t *testing.T) {
	mb, tool, leadID, _, ctx := newTestTeamSetup()
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInReview, true, "")
	mb.taskStore.afterGetTask = func(id uuid.UUID) {
		mb.taskStore.mu.Lock()
		mb.taskStore.tasks[id].Status = store.TeamTaskStatusCompleted
		mb.taskStore.mu.Unlock()
	}

	res := tool.Execute(store.WithAgentID(ctx, leadID), map[string]any{"action": "reject", "task_id": taskID.String(), "text": "redo"})
	mb.taskStore.afterGetTask = nil
	if !res.IsError {
		t.Fatalf("want error when the task was approved concurrently, got %q", res.ForLLM)
	}
	if got := readTask(mb, taskID).Status; got != store.TeamTaskStatusCompleted {
		t.Errorf("status = %q, want completed", got)
	}
	if c := taskContract(t, mb, taskID); len(c.Amendments) != 0 {
		t.Errorf("amendments = %+v, want none", c.Amendments)
	}
	if comments, _ := mb.taskStore.ListTaskComments(ctx, taskID); len(comments) != 0 {
		t.Errorf("comments = %d, want none", len(comments))
	}
	if len(mb.dispatches) != 0 {
		t.Errorf("dispatches = %d, want none", len(mb.dispatches))
	}
}
