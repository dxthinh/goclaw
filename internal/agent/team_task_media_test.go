package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// The shape create_image really returns: MEDIA text plus an explicit result.Media.
func generatedImageResult(path string) *tools.Result {
	result := tools.NewResult("MEDIA:" + path)
	result.Media = []bus.MediaFile{{Path: path, MimeType: "image/png"}}
	return result
}

func runGeneratedImage(t *testing.T, req *RunRequest) []MediaResult {
	t.Helper()
	workspace := t.TempDir()
	ctx := tools.WithToolWorkspace(context.Background(), workspace)
	draft := filepath.Join(workspace, "draft.png")
	if err := os.WriteFile(draft, []byte("draft"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := &runState{}
	(&Loop{}).processToolResult(ctx, state, req, func(AgentEvent) {},
		providers.ToolCall{Name: "create_image"}, "create_image", generatedImageResult(draft), false)
	return state.mediaResults
}

func TestAssignedTaskDoesNotQueueGeneratedDraft(t *testing.T) {
	if media := runGeneratedImage(t, &RunRequest{TeamTaskID: "task"}); len(media) != 0 {
		t.Fatalf("draft queued for delivery: %+v", media)
	}
}

func TestNormalChatStillQueuesGeneratedImage(t *testing.T) {
	if media := runGeneratedImage(t, &RunRequest{}); len(media) != 1 {
		t.Fatalf("media = %+v, want the generated image queued", media)
	}
}

func TestAssignedTaskStillQueuesSendFile(t *testing.T) {
	workspace := t.TempDir()
	ctx := tools.WithToolWorkspace(context.Background(), workspace)
	final := filepath.Join(workspace, "final.png")
	if err := os.WriteFile(final, []byte("final poster"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"path": final}
	send := tools.NewSendFileTool(workspace, true)
	result := send.Execute(ctx, args)
	if result.IsError {
		t.Fatal(result.ForLLM)
	}

	state := &runState{}
	(&Loop{}).processToolResult(ctx, state, &RunRequest{TeamTaskID: "task"}, func(AgentEvent) {},
		providers.ToolCall{Name: send.Name(), Arguments: args}, send.Name(), result, false)

	if len(state.mediaResults) != 1 || state.mediaResults[0].Path != final {
		t.Fatalf("media = %+v, want the final file", state.mediaResults)
	}
}
