package tools

import (
	"errors"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Nộp kết quả chờ user duyệt mà store lỗi thì task không kẹt in_review thiếu
// marker: vẫn in_progress, không có checkpoint_pending, không có tin chờ duyệt;
// lần nộp sau thành công ghi đủ trạng thái và marker cùng lúc.
func TestSubmitForUserApproval_StoreErrorLeavesNoHalfState(t *testing.T) {
	mb, tool, _, _, ctx := newTestTeamSetup()
	mb.approvalChannels = map[string]bool{"telegram": true}
	taskID := makeFeedbackTask(t, mb, store.TeamTaskStatusInProgress, false, "")
	readTask(mb, taskID).Metadata[TaskMetaCheckpoint] = TaskContractCheckpointResult
	mb.taskStore.submitReviewErr = errors.New("db down")

	res := tool.Execute(memberCtx(ctx), map[string]any{"action": "complete", "task_id": taskID.String(), "result": "report ready"})
	if !res.IsError {
		t.Fatalf("complete with store error must fail, got %q", res.ForLLM)
	}
	got := readTask(mb, taskID)
	if got.Status != store.TeamTaskStatusInProgress || TaskCheckpointPending(got) != "" || got.Result != nil {
		t.Fatalf("after failed submit: status=%q pending=%q result=%v, want untouched in_progress",
			got.Status, TaskCheckpointPending(got), got.Result)
	}
	if len(mb.approvalNotices) != 0 {
		t.Errorf("no approval notice expected after failed submit, got %v", mb.approvalNotices)
	}

	mb.taskStore.submitReviewErr = nil
	res = tool.Execute(memberCtx(ctx), map[string]any{"action": "complete", "task_id": taskID.String(), "result": "report ready"})
	if res.IsError || !strings.Contains(res.ForLLM, "user's approval") {
		t.Fatalf("retry complete = %q", res.ForLLM)
	}
	got = readTask(mb, taskID)
	if got.Status != store.TeamTaskStatusInReview || TaskCheckpointPending(got) != CheckpointPendingUser ||
		got.Metadata[TaskMetaCheckpoint] != TaskContractCheckpointResult {
		t.Errorf("after retry: status=%q pending=%q meta=%v, want in_review waiting for user with metadata kept",
			got.Status, TaskCheckpointPending(got), got.Metadata)
	}
	if len(mb.approvalNotices) != 1 {
		t.Errorf("approval notices = %v, want 1", mb.approvalNotices)
	}
}
