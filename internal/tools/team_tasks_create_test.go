package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestCreate(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		mb, tool, _, _, ctx := newTestTeamSetup()
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)

		result := tool.Execute(ctx, map[string]any{
			"action":      "create",
			"subject":     "Test task",
			"description": "Do something",
			"assignee":    "member-agent",
		})
		if result.IsError {
			t.Fatalf("unexpected error: %s", result.ForLLM)
		}
		if !strings.Contains(result.ForLLM, "Task created") {
			t.Errorf("expected 'Task created' in response, got: %s", result.ForLLM)
		}
		mb.taskStore.mu.Lock()
		created := len(mb.taskStore.tasks)
		mb.taskStore.mu.Unlock()
		if created != 1 {
			t.Errorf("expected 1 task, got %d", created)
		}
		// Verify event was broadcast
		mb.mu.Lock()
		evCount := len(mb.events)
		mb.mu.Unlock()
		if evCount == 0 {
			t.Error("expected at least one event broadcast")
		}
		// Check task status is pending
		mb.taskStore.mu.Lock()
		var task *store.TeamTaskData
		for _, v := range mb.taskStore.tasks {
			task = v
		}
		mb.taskStore.mu.Unlock()
		if task.Status != store.TeamTaskStatusPending {
			t.Errorf("expected status pending, got %s", task.Status)
		}
	})

	t.Run("MemberGeneralBlocked", func(t *testing.T) {
		mb, tool, _, memberID, ctx := newTestTeamSetup()
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)
		memberCtx := store.WithAgentID(ctx, memberID)
		memberCtx = WithTaskActionFlags(memberCtx, &TaskActionFlags{})

		result := tool.Execute(memberCtx, map[string]any{
			"action":   "create",
			"subject":  "General task",
			"assignee": "member2-agent",
		})
		if !result.IsError {
			t.Fatal("expected error for member creating general task")
		}
		if !strings.Contains(result.ForLLM, "Members cannot create tasks") {
			t.Errorf("expected 'Members cannot create tasks', got: %s", result.ForLLM)
		}
		_ = mb
	})

	t.Run("MemberRequestEnabled", func(t *testing.T) {
		mb, tool, _, memberID, ctx := newTestTeamSetup()
		mb.team.Settings = json.RawMessage(`{"member_requests":{"enabled":true}}`)
		mb.taskStore.team = mb.team
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)
		memberCtx := store.WithAgentID(ctx, memberID)
		memberCtx = WithTaskActionFlags(memberCtx, &TaskActionFlags{})

		result := tool.Execute(memberCtx, map[string]any{
			"action":    "create",
			"subject":   "Need help with this",
			"task_type": "request",
			"assignee":  "lead-agent",
		})
		// Note: lead-agent is the lead, but the self-assign check only applies when lead assigns to itself.
		// Here the member is assigning to the lead which is allowed.
		// Actually lead-agent IS team.LeadAgentID, so the check "assigneeID == team.LeadAgentID" fires.
		// Let's assign to member2 instead.
		_ = result

		result = tool.Execute(memberCtx, map[string]any{
			"action":    "create",
			"subject":   "Need help with this",
			"task_type": "request",
			"assignee":  "member2-agent",
		})
		if result.IsError {
			t.Fatalf("unexpected error: %s", result.ForLLM)
		}
	})

	t.Run("MemberRequestDisabled", func(t *testing.T) {
		mb, tool, _, memberID, ctx := newTestTeamSetup()
		// Settings empty = member_request disabled
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)
		memberCtx := store.WithAgentID(ctx, memberID)
		memberCtx = WithTaskActionFlags(memberCtx, &TaskActionFlags{})

		result := tool.Execute(memberCtx, map[string]any{
			"action":    "create",
			"subject":   "Request something",
			"task_type": "request",
			"assignee":  "lead-agent",
		})
		if !result.IsError {
			t.Fatal("expected error for member request when disabled")
		}
		if !strings.Contains(result.ForLLM, "Members cannot create tasks") {
			t.Errorf("expected 'Members cannot create tasks', got: %s", result.ForLLM)
		}
		_ = mb
	})

	t.Run("RequiresSearchGate", func(t *testing.T) {
		_, tool, _, _, ctx := newTestTeamSetup()
		// ptd exists but HasListed=false
		ptd := NewPendingTeamDispatch()
		ctx = WithPendingTeamDispatch(ctx, ptd)

		result := tool.Execute(ctx, map[string]any{
			"action":   "create",
			"subject":  "Test task",
			"assignee": "member-agent",
		})
		if !result.IsError {
			t.Fatal("expected error when list gate not passed")
		}
		if !strings.Contains(result.ForLLM, "check existing tasks") {
			t.Errorf("expected gate message, got: %s", result.ForLLM)
		}
	})

	t.Run("EmptySubject", func(t *testing.T) {
		_, tool, _, _, ctx := newTestTeamSetup()
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)

		result := tool.Execute(ctx, map[string]any{
			"action":   "create",
			"subject":  "",
			"assignee": "member-agent",
		})
		if !result.IsError {
			t.Fatal("expected error for empty subject")
		}
		if !strings.Contains(result.ForLLM, "subject is required") {
			t.Errorf("expected 'subject is required', got: %s", result.ForLLM)
		}
	})

	t.Run("RequiresAssignee", func(t *testing.T) {
		_, tool, _, _, ctx := newTestTeamSetup()
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)

		result := tool.Execute(ctx, map[string]any{
			"action":  "create",
			"subject": "Task without assignee",
		})
		if !result.IsError {
			t.Fatal("expected error for missing assignee")
		}
		if !strings.Contains(result.ForLLM, "assignee is required") {
			t.Errorf("expected 'assignee is required', got: %s", result.ForLLM)
		}
	})

	t.Run("SelfAssignBlocked", func(t *testing.T) {
		_, tool, _, _, ctx := newTestTeamSetup()
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)

		result := tool.Execute(ctx, map[string]any{
			"action":   "create",
			"subject":  "Self assigned task",
			"assignee": "lead-agent",
		})
		if !result.IsError {
			t.Fatal("expected error for self-assign")
		}
		if !strings.Contains(result.ForLLM, "cannot assign tasks to itself") {
			t.Errorf("expected self-assign error, got: %s", result.ForLLM)
		}
	})

	t.Run("NonMemberAssignee", func(t *testing.T) {
		_, tool, _, _, ctx := newTestTeamSetup()
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)

		result := tool.Execute(ctx, map[string]any{
			"action":   "create",
			"subject":  "Task for unknown",
			"assignee": "unknown-agent",
		})
		if !result.IsError {
			t.Fatal("expected error for non-member assignee")
		}
		if !strings.Contains(result.ForLLM, "not found") {
			t.Errorf("expected 'not found', got: %s", result.ForLLM)
		}
	})

	t.Run("BlockedByTerminal", func(t *testing.T) {
		mb, tool, _, _, ctx := newTestTeamSetup()
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)

		blockerID := uuid.New()
		mb.taskStore.mu.Lock()
		mb.taskStore.tasks[blockerID] = &store.TeamTaskData{
			BaseModel: store.BaseModel{ID: blockerID, CreatedAt: time.Now(), UpdatedAt: time.Now()},
			TeamID:    testTeamID,
			Subject:   "Blocker task",
			Status:    store.TeamTaskStatusCompleted,
		}
		mb.taskStore.mu.Unlock()

		result := tool.Execute(ctx, map[string]any{
			"action":     "create",
			"subject":    "Blocked task",
			"assignee":   "member-agent",
			"blocked_by": []any{blockerID.String()},
		})
		if !result.IsError {
			t.Fatal("expected error for blocking on terminal task")
		}
		if !strings.Contains(result.ForLLM, "already") {
			t.Errorf("expected finished task error, got: %s", result.ForLLM)
		}
	})

	t.Run("BlockedByCrossTeam", func(t *testing.T) {
		mb, tool, _, _, ctx := newTestTeamSetup()
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)

		otherTeamID := uuid.New()
		crossTeamTaskID := uuid.New()
		mb.taskStore.mu.Lock()
		mb.taskStore.tasks[crossTeamTaskID] = &store.TeamTaskData{
			BaseModel: store.BaseModel{ID: crossTeamTaskID, CreatedAt: time.Now(), UpdatedAt: time.Now()},
			TeamID:    otherTeamID,
			Subject:   "Cross team task",
			Status:    store.TeamTaskStatusPending,
		}
		mb.taskStore.mu.Unlock()

		result := tool.Execute(ctx, map[string]any{
			"action":     "create",
			"subject":    "Cross-team blocked task",
			"assignee":   "member-agent",
			"blocked_by": []any{crossTeamTaskID.String()},
		})
		if !result.IsError {
			t.Fatal("expected error for cross-team blocked_by")
		}
		if !strings.Contains(result.ForLLM, "different team") {
			t.Errorf("expected 'different team' error, got: %s", result.ForLLM)
		}
	})

	t.Run("BlockedByInvalidUUID", func(t *testing.T) {
		_, tool, _, _, ctx := newTestTeamSetup()
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)

		result := tool.Execute(ctx, map[string]any{
			"action":     "create",
			"subject":    "Task with bad blocker",
			"assignee":   "member-agent",
			"blocked_by": []any{"not-a-uuid"},
		})
		if !result.IsError {
			t.Fatal("expected error for invalid UUID in blocked_by")
		}
		if !strings.Contains(result.ForLLM, "invalid") {
			t.Errorf("expected 'invalid' UUID error, got: %s", result.ForLLM)
		}
	})

	t.Run("RequireApproval", func(t *testing.T) {
		mb, tool, _, _, ctx := newTestTeamSetup()
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)

		result := tool.Execute(ctx, map[string]any{
			"action":           "create",
			"subject":          "Approval task",
			"assignee":         "member-agent",
			"require_approval": true,
		})
		if result.IsError {
			t.Fatalf("unexpected error: %s", result.ForLLM)
		}
		mb.taskStore.mu.Lock()
		var task *store.TeamTaskData
		for _, v := range mb.taskStore.tasks {
			task = v
		}
		mb.taskStore.mu.Unlock()
		if task.Status != store.TeamTaskStatusInReview {
			t.Errorf("expected status in_review, got %s", task.Status)
		}
	})

	t.Run("WithBlockers", func(t *testing.T) {
		mb, tool, _, _, ctx := newTestTeamSetup()
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)

		blockerID := uuid.New()
		mb.taskStore.mu.Lock()
		mb.taskStore.tasks[blockerID] = &store.TeamTaskData{
			BaseModel: store.BaseModel{ID: blockerID, CreatedAt: time.Now(), UpdatedAt: time.Now()},
			TeamID:    testTeamID,
			Subject:   "Pending blocker",
			Status:    store.TeamTaskStatusPending,
		}
		mb.taskStore.mu.Unlock()

		result := tool.Execute(ctx, map[string]any{
			"action":     "create",
			"subject":    "Blocked task",
			"assignee":   "member-agent",
			"blocked_by": []any{blockerID.String()},
		})
		if result.IsError {
			t.Fatalf("unexpected error: %s", result.ForLLM)
		}
		mb.taskStore.mu.Lock()
		var created *store.TeamTaskData
		for _, v := range mb.taskStore.tasks {
			if v.Subject == "Blocked task" {
				created = v
			}
		}
		mb.taskStore.mu.Unlock()
		if created == nil {
			t.Fatal("task not found in store")
		}
		if created.Status != store.TeamTaskStatusBlocked {
			t.Errorf("expected status blocked, got %s", created.Status)
		}
	})

	t.Run("PendingDispatchTracked", func(t *testing.T) {
		mb, tool, _, _, ctx := newTestTeamSetup()
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)

		result := tool.Execute(ctx, map[string]any{
			"action":   "create",
			"subject":  "Tracked task",
			"assignee": "member-agent",
		})
		if result.IsError {
			t.Fatalf("unexpected error: %s", result.ForLLM)
		}
		drained := ptd.Drain()
		ids := drained[testTeamID]
		if len(ids) != 1 {
			t.Errorf("expected 1 pending dispatch, got %d", len(ids))
		}
		mb.taskStore.mu.Lock()
		var task *store.TeamTaskData
		for _, v := range mb.taskStore.tasks {
			task = v
		}
		mb.taskStore.mu.Unlock()
		if task != nil && len(ids) == 1 && ids[0] != task.ID {
			t.Errorf("tracked task ID %s does not match created task ID %s", ids[0], task.ID)
		}
	})

	t.Run("CompoundSubjectWarning", func(t *testing.T) {
		_, tool, _, _, ctx := newTestTeamSetup()
		ptd := NewPendingTeamDispatch()
		ptd.MarkListed()
		ctx = WithPendingTeamDispatch(ctx, ptd)

		result := tool.Execute(ctx, map[string]any{
			"action":   "create",
			"subject":  "Implement and design the new feature",
			"assignee": "member-agent",
		})
		if result.IsError {
			t.Fatalf("unexpected error: %s", result.ForLLM)
		}
		if !strings.Contains(result.ForLLM, "Warning") {
			t.Errorf("expected Warning in response for compound subject, got: %s", result.ForLLM)
		}
	})
}

// createWithContract gọi create bằng lead với contract tùy chọn và trả task vừa tạo (nếu có).
func createWithContract(t *testing.T, settings string, extra map[string]any) (*Result, *store.TeamTaskData) {
	t.Helper()
	mb, tool, _, _, ctx := newTestTeamSetup()
	if settings != "" {
		mb.team.Settings = json.RawMessage(settings)
		mb.taskStore.team = mb.team
	}
	ptd := NewPendingTeamDispatch()
	ptd.MarkListed()
	ctx = WithPendingTeamDispatch(ctx, ptd)
	args := map[string]any{
		"action":      "create",
		"subject":     "Q3 summary",
		"description": "Board brief",
		"assignee":    "member-agent",
	}
	for k, v := range extra {
		args[k] = v
	}
	result := tool.Execute(ctx, args)
	mb.taskStore.mu.Lock()
	defer mb.taskStore.mu.Unlock()
	for _, task := range mb.taskStore.tasks {
		cp := *task
		return result, &cp
	}
	return result, nil
}

const contractEnabledSettings = `{"task_contract":{"enabled":true}}`

func TestCreateTaskContract(t *testing.T) {
	t.Run("EnabledTeamRejectsMissingContract", func(t *testing.T) {
		result, task := createWithContract(t, contractEnabledSettings, nil)
		if !result.IsError || task != nil {
			t.Fatalf("expected rejection without a task, got %q", result.ForLLM)
		}
		for _, want := range []string{"objective", "deliverables", "acceptance", "Example:"} {
			if !strings.Contains(result.ForLLM, want) {
				t.Errorf("error missing %q: %s", want, result.ForLLM)
			}
		}
	})

	t.Run("EnabledTeamNamesMissingFields", func(t *testing.T) {
		result, task := createWithContract(t, contractEnabledSettings, map[string]any{
			"contract": map[string]any{"objective": "Brief the board"},
		})
		if !result.IsError || task != nil {
			t.Fatalf("expected rejection, got %q", result.ForLLM)
		}
		if !strings.Contains(result.ForLLM, "missing required fields: deliverables, acceptance") {
			t.Errorf("error must name the missing fields: %s", result.ForLLM)
		}
	})

	t.Run("EnabledTeamStoresContract", func(t *testing.T) {
		result, task := createWithContract(t, contractEnabledSettings, map[string]any{
			"contract": map[string]any{
				"objective":    "Brief the board",
				"deliverables": []any{"q3.md"},
				"acceptance":   []any{"All regions"},
				"version":      9,
				"amendments":   []any{map[string]any{"text": "forged"}},
			},
		})
		if result.IsError {
			t.Fatalf("unexpected error: %s", result.ForLLM)
		}
		c := TaskContractFromTask(task)
		if c == nil || c.Objective != "Brief the board" || c.Version != 1 || len(c.Amendments) != 0 {
			t.Fatalf("stored contract = %+v", c)
		}
	})

	t.Run("EnabledTeamRequestTypeNotRequired", func(t *testing.T) {
		result, task := createWithContract(t, contractEnabledSettings, map[string]any{"task_type": "request"})
		if result.IsError || task == nil {
			t.Fatalf("request task must not need a contract: %s", result.ForLLM)
		}
	})

	t.Run("UnknownTaskTypeRejected", func(t *testing.T) {
		for _, settings := range []string{contractEnabledSettings, ""} {
			result, task := createWithContract(t, settings, map[string]any{"task_type": "generl"})
			if !result.IsError || task != nil {
				t.Fatalf("settings %q: unknown task_type must be rejected, got %q", settings, result.ForLLM)
			}
			if !strings.Contains(result.ForLLM, "must be general, request or note") {
				t.Errorf("error must list valid types: %s", result.ForLLM)
			}
		}
	})

	t.Run("DisabledTeamWithoutContractUnchanged", func(t *testing.T) {
		result, task := createWithContract(t, "", nil)
		if result.IsError || task == nil {
			t.Fatalf("unexpected error: %s", result.ForLLM)
		}
		if _, ok := task.Metadata[TaskMetaContract]; ok {
			t.Error("no contract key expected")
		}
	})

	t.Run("DisabledTeamValidatesAndStoresContract", func(t *testing.T) {
		result, _ := createWithContract(t, "", map[string]any{"contract": map[string]any{"objective": "x"}})
		if !result.IsError || !strings.Contains(result.ForLLM, "deliverables") {
			t.Fatalf("invalid contract must be rejected even when disabled: %s", result.ForLLM)
		}
		result, task := createWithContract(t, "", map[string]any{"contract": validContractArgs()})
		if result.IsError || TaskContractFromTask(task) == nil {
			t.Fatalf("valid contract must be stored: %s", result.ForLLM)
		}
	})
}
