package tools

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Member ở kênh dashboard không được reject task (tự đưa task về chạy lại).
func TestReject_MemberOnDashboardChannelBlocked(t *testing.T) {
	mb, tool, _, memberID, ctx := newTestTeamSetup()
	taskID := makeTask(mb, testTeamID, memberID, store.TeamTaskStatusInReview)

	mCtx := store.WithAgentID(ctx, memberID)
	mCtx = WithToolChannel(mCtx, ChannelDashboard)
	mCtx = WithTaskActionFlags(mCtx, &TaskActionFlags{})
	result := tool.Execute(mCtx, map[string]any{
		"action":  "reject",
		"task_id": taskID.String(),
		"text":    "redo",
	})

	if !result.IsError {
		t.Fatal("expected error: member must not reject on the dashboard channel")
	}
	if got := readTask(mb, taskID).Status; got != store.TeamTaskStatusInReview {
		t.Errorf("status = %q, want in_review", got)
	}
}

// Member khác không được complete task của owner khác.
func TestComplete_NonOwnerMemberBlocked(t *testing.T) {
	mb, tool, _, memberID, ctx := newTestTeamSetup()
	taskID := makeTask(mb, testTeamID, memberID, store.TeamTaskStatusInProgress)

	otherCtx := store.WithAgentID(ctx, testMember2ID)
	otherCtx = WithToolChannel(otherCtx, "telegram")
	otherCtx = WithTaskActionFlags(otherCtx, &TaskActionFlags{})
	result := tool.Execute(otherCtx, map[string]any{
		"action":  "complete",
		"task_id": taskID.String(),
		"result":  "done by someone else",
	})

	if !result.IsError {
		t.Fatal("expected error: only the owner or the lead can complete a task")
	}
	if got := readTask(mb, taskID).Status; got != store.TeamTaskStatusInProgress {
		t.Errorf("status = %q, want in_progress", got)
	}
}

func TestComplete_OwnerAndLeadAllowed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		actor uuid.UUID
	}{{"Owner", testMemberID}, {"Lead", testLeadID}} {
		t.Run(tc.name, func(t *testing.T) {
			mb, tool, _, memberID, ctx := newTestTeamSetup()
			taskID := makeTask(mb, testTeamID, memberID, store.TeamTaskStatusInProgress)

			aCtx := store.WithAgentID(ctx, tc.actor)
			aCtx = WithToolChannel(aCtx, "telegram")
			aCtx = WithTaskActionFlags(aCtx, &TaskActionFlags{})
			result := tool.Execute(aCtx, map[string]any{
				"action":  "complete",
				"task_id": taskID.String(),
				"result":  "done",
			})
			if result.IsError {
				t.Fatalf("unexpected error: %s", result.ForLLM)
			}
			if got := readTask(mb, taskID).Status; got != store.TeamTaskStatusCompleted {
				t.Errorf("status = %q, want completed", got)
			}
		})
	}
}

// Retry là dispatch có chủ ý: bộ đếm dispatch phải về 0 để circuit breaker
// không đánh fail ngay task vừa được lead mở lại.
func TestRetry_ResetsDispatchCount(t *testing.T) {
	mb, tool, leadID, memberID, ctx := newTestTeamSetup()
	taskID := makeTask(mb, testTeamID, memberID, store.TeamTaskStatusFailed)
	readTask(mb, taskID).Metadata = map[string]any{"dispatch_count": float64(maxTaskDispatches)}

	lCtx := store.WithAgentID(ctx, leadID)
	lCtx = WithTaskActionFlags(lCtx, &TaskActionFlags{})
	result := tool.Execute(lCtx, map[string]any{"action": "retry", "task_id": taskID.String()})
	if result.IsError {
		t.Fatalf("retry: %s", result.ForLLM)
	}
	assertLastDispatchCountZero(t, mb)
}

func TestReject_ResetsDispatchCount(t *testing.T) {
	mb, tool, leadID, memberID, ctx := newTestTeamSetup()
	taskID := makeTask(mb, testTeamID, memberID, store.TeamTaskStatusInReview)
	readTask(mb, taskID).Metadata = map[string]any{"dispatch_count": float64(maxTaskDispatches)}

	lCtx := store.WithAgentID(ctx, leadID)
	lCtx = WithTaskActionFlags(lCtx, &TaskActionFlags{})
	result := tool.Execute(lCtx, map[string]any{"action": "reject", "task_id": taskID.String(), "text": "fix it"})
	if result.IsError {
		t.Fatalf("reject: %s", result.ForLLM)
	}
	assertLastDispatchCountZero(t, mb)
}

func assertLastDispatchCountZero(t *testing.T, mb *mockBackend) {
	t.Helper()
	mb.mu.Lock()
	defer mb.mu.Unlock()
	if len(mb.dispatches) == 0 {
		t.Fatal("expected a dispatch")
	}
	d := mb.dispatches[len(mb.dispatches)-1]
	switch v := d.DispatchCount.(type) {
	case int:
		if v == 0 {
			return
		}
	case float64:
		if v == 0 {
			return
		}
	}
	t.Errorf("dispatch_count at dispatch = %v, want 0", d.DispatchCount)
}

// Tin escalation của blocker phải nêu các task đang bị chặn bởi task vừa fail,
// vì chúng sẽ đứng ở blocked tới khi lead retry hoặc cancel.
func TestBlockerEscalation_ListsBlockedDependents(t *testing.T) {
	mb, tool, _, memberID, ctx := newTestTeamSetup()
	taskID := makeTask(mb, testTeamID, memberID, store.TeamTaskStatusInProgress)
	depID := makeTask(mb, testTeamID, testMember2ID, store.TeamTaskStatusBlocked)
	dep := readTask(mb, depID)
	dep.Subject = "downstream work"
	dep.BlockedBy = []uuid.UUID{taskID}
	dep.TaskNumber = 7

	mCtx := store.WithAgentID(ctx, memberID)
	mCtx = WithTaskActionFlags(mCtx, &TaskActionFlags{})
	mCtx = WithTeamTaskID(mCtx, taskID.String())
	result := tool.Execute(mCtx, map[string]any{
		"action": "comment", "task_id": taskID.String(), "text": "no access", "type": "blocker",
	})
	if result.IsError {
		t.Fatalf("blocker comment: %s", result.ForLLM)
	}

	mb.mu.Lock()
	defer mb.mu.Unlock()
	if len(mb.inbound) == 0 {
		t.Fatal("expected escalation message to lead")
	}
	msg := mb.inbound[len(mb.inbound)-1].Content
	if !strings.Contains(msg, "#7") || !strings.Contains(msg, "downstream work") {
		t.Errorf("escalation không nêu task bị chặn:\n%s", msg)
	}
	if !strings.Contains(msg, `action="cancel"`) {
		t.Errorf("escalation phải nêu cách cancel:\n%s", msg)
	}
}
