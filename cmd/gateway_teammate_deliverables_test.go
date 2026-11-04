package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/agent"
	"github.com/nextlevelbuilder/goclaw/internal/scheduler"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// stubAttachmentStore serves a task's attachments to buildTeammateAnnounce.
type stubAttachmentStore struct {
	store.TeamStore
	attachments []store.TeamTaskAttachmentData
	listErr     error
}

func (s *stubAttachmentStore) ListTaskAttachments(context.Context, uuid.UUID) ([]store.TeamTaskAttachmentData, error) {
	return s.attachments, s.listErr
}

func (s *stubAttachmentStore) ListRecentTaskComments(context.Context, uuid.UUID, int) ([]store.TeamTaskCommentData, error) {
	return nil, nil
}

func writeAnnounceFile(t *testing.T, name string) string {
	t.Helper()
	// Stored deliverable paths are canonical, and TempDir may sit behind a symlink.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func deliverableAttachment(path string) store.TeamTaskAttachmentData {
	return store.TeamTaskAttachmentData{Path: path, MimeType: "image/png", Metadata: json.RawMessage(store.TaskAttachmentDeliverableMetadata)}
}

func announceFor(t *testing.T, result *agent.RunResult, attachments ...store.TeamTaskAttachmentData) (string, []agent.MediaResult, bool) {
	t.Helper()
	deps := &ConsumerDeps{TeamStore: &stubAttachmentStore{attachments: attachments}}
	meta := map[string]string{tools.MetaTeamTaskID: uuid.NewString()}
	return buildTeammateAnnounce(context.Background(), scheduler.RunOutcome{Result: result}, "member", meta, deps)
}

// Task #4 of the incident: nine drafts attached automatically, the real posters
// declared on complete. Only the posters may go out.
func TestTeammateAnnounceSendsDeclaredFilesNotDrafts(t *testing.T) {
	draft := writeAnnounceFile(t, "draft.png")
	poster := writeAnnounceFile(t, "poster.png")

	content, media, ok := announceFor(t, &agent.RunResult{Content: "done"},
		store.TeamTaskAttachmentData{Path: draft, Metadata: json.RawMessage(`{}`)},
		deliverableAttachment(poster),
	)

	if !ok || len(media) != 1 || media[0].Path != poster {
		t.Fatalf("media = %+v, want only the declared poster", media)
	}
	if !strings.Contains(content, draft) || strings.Contains(content, "did not declare") {
		t.Fatalf("content = %q", content)
	}
}

func TestTeammateAnnounceDoesNotDuplicateSendFile(t *testing.T) {
	poster := writeAnnounceFile(t, "poster.png")

	_, media, _ := announceFor(t, &agent.RunResult{Content: "done", Media: []agent.MediaResult{{Path: poster}}},
		deliverableAttachment(poster))

	if len(media) != 1 {
		t.Fatalf("media = %+v, want one entry", media)
	}
}

// Agreed behavior: nothing declared means nothing is sent, and the lead is told
// where the files are so it can decide.
func TestTeammateAnnounceWithoutDeclaredFilesTellsLead(t *testing.T) {
	draft := writeAnnounceFile(t, "draft.png")

	content, media, ok := announceFor(t, &agent.RunResult{Content: "done"},
		store.TeamTaskAttachmentData{Path: draft, Metadata: json.RawMessage(`{}`)})

	if !ok || len(media) != 0 {
		t.Fatalf("media = %+v, want none", media)
	}
	if !strings.Contains(content, draft) || !strings.Contains(content, "did not declare final files") {
		t.Fatalf("content = %q", content)
	}
}

// Declared files are output: a member ending in NO_REPLY must not lose them.
func TestTeammateAnnounceSilentReplyKeepsDeclaredFiles(t *testing.T) {
	poster := writeAnnounceFile(t, "poster.png")

	_, media, ok := announceFor(t, &agent.RunResult{Content: "NO_REPLY"}, deliverableAttachment(poster))

	if !ok || len(media) != 1 {
		t.Fatalf("ok = %v, media = %+v", ok, media)
	}
}

func TestTeammateAnnounceReportsDeclaredFileGone(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone.png")

	content, media, _ := announceFor(t, &agent.RunResult{Content: "done"}, deliverableAttachment(gone))

	if len(media) != 0 || !strings.Contains(content, "failed the delivery check") {
		t.Fatalf("media = %+v, content = %q", media, content)
	}
}

// Only MarkTaskDeliverables tags a row, after validating its path. Plain metadata
// must never read as a deliverable.
func TestAttachmentMetadataIsNotDeliverableUnlessTagged(t *testing.T) {
	for _, raw := range []string{``, `{}`, `{"role":"attachment"}`, `not json`} {
		att := store.TeamTaskAttachmentData{Metadata: json.RawMessage(raw)}
		if att.IsDeliverable() {
			t.Errorf("metadata %q read as deliverable", raw)
		}
	}
}

// The file was valid when the task completed, then swapped before the announce. The
// gateway must not follow the swap and upload what it points to.
func TestTeammateAnnounceRefusesDeliverableSwappedAfterValidation(t *testing.T) {
	secret := writeAnnounceFile(t, "secret.txt")

	swaps := map[string]func(t *testing.T, path string){
		"file replaced by symlink": func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(secret, path); err != nil {
				t.Fatal(err)
			}
		},
		"file replaced by hardlink": func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(secret, path); err != nil {
				t.Skipf("hardlink not available: %v", err)
			}
		},
		"parent folder replaced by symlink": func(t *testing.T, path string) {
			dir := filepath.Dir(path)
			if err := os.Rename(dir, dir+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(dir+".real", dir); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, swap := range swaps {
		t.Run(name, func(t *testing.T) {
			poster := writeAnnounceFile(t, "poster.png")
			swap(t, poster)

			content, media, ok := announceFor(t, &agent.RunResult{Content: "done"}, deliverableAttachment(poster))

			if !ok || len(media) != 0 {
				t.Fatalf("media = %+v, the swapped file must not be sent", media)
			}
			if !strings.Contains(content, "failed the delivery check") {
				t.Fatalf("content = %q", content)
			}
		})
	}
}

// The member completed the task, then a later model call failed. The task stays
// completed and no other announce follows, so the declared files must go out here.
func TestTeammateAnnounceKeepsDeclaredFilesAfterRunError(t *testing.T) {
	poster := writeAnnounceFile(t, "poster.png")
	deps := &ConsumerDeps{TeamStore: &stubAttachmentStore{attachments: []store.TeamTaskAttachmentData{deliverableAttachment(poster)}}}
	meta := map[string]string{tools.MetaTeamTaskID: uuid.NewString()}

	content, media, ok := buildTeammateAnnounce(context.Background(), scheduler.RunOutcome{Err: errors.New("model call failed")}, "member", meta, deps)

	if !ok || len(media) != 1 || media[0].Path != poster {
		t.Fatalf("ok = %v, media = %+v", ok, media)
	}
	if !strings.HasPrefix(content, "[FAILED]") || !strings.Contains(content, "completed before this error") {
		t.Fatalf("content = %q", content)
	}
}

func TestTeammateAnnounceRunErrorWithoutDeclaredFilesStaysFailed(t *testing.T) {
	deps := &ConsumerDeps{TeamStore: &stubAttachmentStore{}}
	meta := map[string]string{tools.MetaTeamTaskID: uuid.NewString()}

	content, media, ok := buildTeammateAnnounce(context.Background(), scheduler.RunOutcome{Err: errors.New("model call failed")}, "member", meta, deps)

	if !ok || len(media) != 0 || strings.Contains(content, "completed before this error") {
		t.Fatalf("ok = %v, media = %+v, content = %q", ok, media, content)
	}
}

// A failed read must not look like "no files": the lead has to hear about it, even
// when the member finished with NO_REPLY.
func TestTeammateAnnounceReportsAttachmentReadFailure(t *testing.T) {
	deps := &ConsumerDeps{TeamStore: &stubAttachmentStore{listErr: errors.New("db unavailable")}}
	meta := map[string]string{tools.MetaTeamTaskID: uuid.NewString()}

	for _, reply := range []string{"done", "NO_REPLY"} {
		outcome := scheduler.RunOutcome{Result: &agent.RunResult{Content: reply}}
		content, media, ok := buildTeammateAnnounce(context.Background(), outcome, "member", meta, deps)

		if !ok || len(media) != 0 || !strings.Contains(content, "Could not read this task's attachments") {
			t.Fatalf("reply %q: ok = %v, media = %+v, content = %q", reply, ok, media, content)
		}
	}
}
