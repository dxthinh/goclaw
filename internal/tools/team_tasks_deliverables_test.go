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
