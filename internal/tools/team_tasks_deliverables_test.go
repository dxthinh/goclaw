package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func writeDeliverable(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// completeWithDeliverables runs action=complete as the assigned member, with the
// tool context a dispatched run has: its workspace is the team workspace.
func completeWithDeliverables(t *testing.T, workspace string, deliverables []any) (*mockBackend, uuid.UUID, *Result) {
	t.Helper()
	mb, tool, _, memberID, ctx := newTestTeamSetup()
	taskID := uuid.New()
	preloadTask(mb, &store.TeamTaskData{
		BaseModel:    store.BaseModel{ID: taskID},
		TeamID:       testTeamID,
		Subject:      "work",
		Status:       store.TeamTaskStatusInProgress,
		OwnerAgentID: &memberID,
	})
	ctx = store.WithAgentID(ctx, memberID)
	ctx = WithTaskActionFlags(ctx, &TaskActionFlags{})
	ctx = WithTeamTaskID(ctx, taskID.String())
	ctx = WithToolWorkspace(ctx, workspace)
	ctx = WithToolTeamWorkspace(ctx, workspace)
	return mb, taskID, tool.Execute(ctx, map[string]any{
		"action":       "complete",
		"task_id":      taskID.String(),
		"result":       "done",
		"deliverables": deliverables,
	})
}

func TestCompleteRegistersDeliverables(t *testing.T) {
	ws := t.TempDir()
	final := writeDeliverable(t, ws, "out/final.png", "poster")

	mb, taskID, res := completeWithDeliverables(t, ws, []any{final})

	if res.IsError {
		t.Fatalf("complete failed: %s", res.ForLLM)
	}
	if !strings.Contains(res.ForLLM, "Deliverables registered: 1") {
		t.Fatalf("result = %q", res.ForLLM)
	}
	atts, err := mb.Store().ListTaskAttachments(context.Background(), taskID)
	if err != nil || len(atts) != 1 {
		t.Fatalf("attachments = %+v, err = %v", atts, err)
	}
	if !atts[0].IsDeliverable() || atts[0].FileSize != int64(len("poster")) {
		t.Fatalf("attachment = %+v", atts[0])
	}
}

// A bad path must leave the task open so the model can call complete again.
func TestCompleteRejectsBadDeliverablesAndKeepsTaskOpen(t *testing.T) {
	ws := t.TempDir()
	good := writeDeliverable(t, ws, "final.png", "poster")
	outside := writeDeliverable(t, t.TempDir(), "secret.txt", "secret")
	empty := writeDeliverable(t, ws, "empty.png", "")
	denied := writeDeliverable(t, ws, "memory/notes.md", "private")
	config := writeDeliverable(t, ws, "config.json", "{}")

	cases := map[string]string{
		"outside workspace": outside,
		"missing file":      filepath.Join(ws, "nope.png"),
		"empty file":        empty,
		"denied folder":     denied,
		"denied file":       config,
		"directory":         ws,
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			mb, taskID, res := completeWithDeliverables(t, ws, []any{good, bad})

			if !res.IsError || !strings.Contains(res.ForLLM, bad) {
				t.Fatalf("result = %+v, want an error naming %s", res, bad)
			}
			if task := readTask(mb, taskID); task.Status != store.TeamTaskStatusInProgress {
				t.Fatalf("status = %q, task must stay open", task.Status)
			}
			atts, _ := mb.Store().ListTaskAttachments(context.Background(), taskID)
			if len(atts) != 0 {
				t.Fatalf("nothing may be registered on a rejected call: %+v", atts)
			}
		})
	}
}

func TestCompleteRejectsMalformedDeliverables(t *testing.T) {
	ctx := WithToolWorkspace(context.Background(), t.TempDir())
	if _, err := resolveTaskDeliverables(ctx, "/a.png"); err == nil {
		t.Fatal("a bare string must be rejected")
	}
	if _, err := resolveTaskDeliverables(ctx, []any{42}); err == nil {
		t.Fatal("a non-string item must be rejected")
	}
	over := make([]any, maxTaskDeliverables+1)
	if _, err := resolveTaskDeliverables(ctx, over); err == nil {
		t.Fatal("over the limit must be rejected")
	}
}

func TestCompleteWithoutDeliverablesKeepsCurrentBehavior(t *testing.T) {
	mb, tool, _, memberID, ctx := newTestTeamSetup()
	taskID := uuid.New()
	preloadTask(mb, &store.TeamTaskData{
		BaseModel:    store.BaseModel{ID: taskID},
		TeamID:       testTeamID,
		Subject:      "work",
		Status:       store.TeamTaskStatusInProgress,
		OwnerAgentID: &memberID,
	})
	ctx = WithTaskActionFlags(store.WithAgentID(ctx, memberID), &TaskActionFlags{})

	res := tool.Execute(ctx, map[string]any{"action": "complete", "task_id": taskID.String(), "result": "done"})

	if res.IsError || strings.Contains(res.ForLLM, "Deliverables") {
		t.Fatalf("result = %+v", res)
	}
}

func TestCompleteDeliverablesAllowedInLiteEdition(t *testing.T) {
	if !(LiteTeamPolicy{}).IsAllowed("complete") {
		t.Fatal("deliverables travel on complete, which Lite must allow")
	}
}

func TestCompleteRejectsHardlinkDeliverable(t *testing.T) {
	ws := t.TempDir()
	real := writeDeliverable(t, ws, "real.png", "poster")
	hard := filepath.Join(ws, "hard.png")
	if err := os.Link(real, hard); err != nil {
		t.Skipf("hardlink not available: %v", err)
	}

	_, _, res := completeWithDeliverables(t, ws, []any{hard})

	if !res.IsError || !strings.Contains(res.ForLLM, hard) {
		t.Fatalf("result = %+v, want the hardlink rejected", res)
	}
}

func runCompleteAs(t *testing.T, callerID uuid.UUID, ownerID uuid.UUID, status string, deliverable bool) (*mockBackend, uuid.UUID, *Result) {
	t.Helper()
	ws := t.TempDir()
	mb, tool, _, _, ctx := newTestTeamSetup()
	taskID := uuid.New()
	preloadTask(mb, &store.TeamTaskData{
		BaseModel:    store.BaseModel{ID: taskID},
		TeamID:       testTeamID,
		Subject:      "work",
		Status:       status,
		OwnerAgentID: &ownerID,
	})
	ctx = WithTaskActionFlags(store.WithAgentID(ctx, callerID), &TaskActionFlags{})
	ctx = WithTeamTaskID(ctx, taskID.String())
	ctx = WithToolWorkspace(ctx, ws)
	ctx = WithToolTeamWorkspace(ctx, ws)
	args := map[string]any{"action": "complete", "task_id": taskID.String(), "result": "done"}
	if deliverable {
		args["deliverables"] = []any{writeDeliverable(t, ws, "final.png", "poster")}
	}
	return mb, taskID, tool.Execute(ctx, args)
}

func TestDeliverablesRestrictedToOwnerOrLead(t *testing.T) {
	cases := []struct {
		name      string
		caller    uuid.UUID
		status    string
		wantAdded bool
	}{
		{"owner on active task", testMemberID, store.TeamTaskStatusInProgress, true},
		{"owner on completed task", testMemberID, store.TeamTaskStatusCompleted, true},
		{"lead on active task", testLeadID, store.TeamTaskStatusInProgress, true},
		{"teammate on active task", testMember2ID, store.TeamTaskStatusInProgress, false},
		{"teammate on completed task", testMember2ID, store.TeamTaskStatusCompleted, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mb, taskID, res := runCompleteAs(t, tc.caller, testMemberID, tc.status, true)

			atts, _ := mb.Store().ListTaskAttachments(context.Background(), taskID)
			if tc.wantAdded {
				if res.IsError || len(atts) != 1 {
					t.Fatalf("result = %+v, attachments = %+v", res, atts)
				}
				return
			}
			if !res.IsError || len(atts) != 0 {
				t.Fatalf("result = %+v, attachments = %+v, want rejected", res, atts)
			}
			if task := readTask(mb, taskID); task.Status != tc.status {
				t.Fatalf("status = %q, a rejected call must not change the task", task.Status)
			}
		})
	}
}

// The owner check applies to deliverables only: who may complete a task is unchanged.
func TestCompleteWithoutDeliverablesKeepsPermissions(t *testing.T) {
	_, _, res := runCompleteAs(t, testMember2ID, testMemberID, store.TeamTaskStatusInProgress, false)
	if res.IsError {
		t.Fatalf("result = %+v", res)
	}
}

// A lead may not reach into another team's task, even one that already completed.
func TestDeliverablesRejectedForTaskOfAnotherTeam(t *testing.T) {
	ws := t.TempDir()
	mb, tool, _, _, ctx := newTestTeamSetup()
	taskID := uuid.New()
	preloadTask(mb, &store.TeamTaskData{
		BaseModel: store.BaseModel{ID: taskID},
		TeamID:    uuid.New(),
		Subject:   "foreign",
		Status:    store.TeamTaskStatusCompleted,
	})
	ctx = WithTaskActionFlags(store.WithAgentID(ctx, testLeadID), &TaskActionFlags{})
	ctx = WithTeamTaskID(ctx, taskID.String())
	ctx = WithToolWorkspace(ctx, ws)
	ctx = WithToolTeamWorkspace(ctx, ws)

	res := tool.Execute(ctx, map[string]any{
		"action":       "complete",
		"task_id":      taskID.String(),
		"result":       "done",
		"deliverables": []any{writeDeliverable(t, ws, "final.png", "poster")},
	})

	if !res.IsError || !strings.Contains(res.ForLLM, "does not belong to your team") {
		t.Fatalf("result = %+v", res)
	}
	atts, _ := mb.Store().ListTaskAttachments(context.Background(), taskID)
	if len(atts) != 0 {
		t.Fatalf("attachments = %+v, want none on a foreign task", atts)
	}
}

// Declared files are sent only by the announce of the run working on the task. A
// call from any other run would store them where nothing sends them, so it must be
// rejected before the task changes.
func TestDeliverablesRejectedOutsideTheTaskRun(t *testing.T) {
	cases := map[string]func(ctx context.Context, taskID uuid.UUID) context.Context{
		"lead run without task flags": func(ctx context.Context, taskID uuid.UUID) context.Context {
			return WithTeamTaskID(WithTaskActionFlags(ctx, nil), taskID.String())
		},
		"member run for another task": func(ctx context.Context, _ uuid.UUID) context.Context {
			return WithTeamTaskID(WithTaskActionFlags(ctx, &TaskActionFlags{}), uuid.NewString())
		},
		"run without a task": func(ctx context.Context, _ uuid.UUID) context.Context {
			return WithTaskActionFlags(ctx, &TaskActionFlags{})
		},
	}
	for _, status := range []string{store.TeamTaskStatusInProgress, store.TeamTaskStatusCompleted} {
		for name, runCtx := range cases {
			t.Run(status+"/"+name, func(t *testing.T) {
				ws := t.TempDir()
				mb, tool, _, memberID, ctx := newTestTeamSetup()
				taskID := uuid.New()
				preloadTask(mb, &store.TeamTaskData{
					BaseModel:    store.BaseModel{ID: taskID},
					TeamID:       testTeamID,
					Subject:      "work",
					Status:       status,
					OwnerAgentID: &memberID,
				})
				ctx = runCtx(store.WithAgentID(ctx, memberID), taskID)
				ctx = WithToolWorkspace(ctx, ws)
				ctx = WithToolTeamWorkspace(ctx, ws)

				res := tool.Execute(ctx, map[string]any{
					"action":       "complete",
					"task_id":      taskID.String(),
					"result":       "done",
					"deliverables": []any{writeDeliverable(t, ws, "final.png", "poster")},
				})

				if !res.IsError || !strings.Contains(res.ForLLM, "send_file") {
					t.Fatalf("result = %+v, want rejected with a send_file hint", res)
				}
				if atts, _ := mb.Store().ListTaskAttachments(context.Background(), taskID); len(atts) != 0 {
					t.Fatalf("attachments = %+v, want none", atts)
				}
				if task := readTask(mb, taskID); task.Status != status {
					t.Fatalf("status = %q, a rejected call must not change the task", task.Status)
				}
			})
		}
	}
}

// The run records what it declared, which is all its announce will send. This holds
// when the task was already completed: the same run's announce still follows.
func TestDeliverablesRecordedOnTheRun(t *testing.T) {
	for _, status := range []string{store.TeamTaskStatusInProgress, store.TeamTaskStatusCompleted} {
		t.Run(status, func(t *testing.T) {
			ws := t.TempDir()
			mb, tool, _, memberID, ctx := newTestTeamSetup()
			taskID := uuid.New()
			preloadTask(mb, &store.TeamTaskData{
				BaseModel:    store.BaseModel{ID: taskID},
				TeamID:       testTeamID,
				Subject:      "work",
				Status:       status,
				OwnerAgentID: &memberID,
			})
			flags := &TaskActionFlags{}
			ctx = WithTaskActionFlags(store.WithAgentID(ctx, memberID), flags)
			ctx = WithTeamTaskID(ctx, taskID.String())
			ctx = WithToolWorkspace(ctx, ws)
			ctx = WithToolTeamWorkspace(ctx, ws)
			final := writeDeliverable(t, ws, "final.png", "poster")

			res := tool.Execute(ctx, map[string]any{
				"action":       "complete",
				"task_id":      taskID.String(),
				"result":       "done",
				"deliverables": []any{final},
			})

			if res.IsError || !strings.Contains(res.ForLLM, "Deliverables registered: 1") {
				t.Fatalf("result = %+v", res)
			}
			atts, _ := mb.Store().ListTaskAttachments(context.Background(), taskID)
			if len(atts) != 1 || len(flags.Deliverables) != 1 || flags.Deliverables[0] != atts[0].Path {
				t.Fatalf("flags.Deliverables = %v, attachments = %+v", flags.Deliverables, atts)
			}
		})
	}
}
