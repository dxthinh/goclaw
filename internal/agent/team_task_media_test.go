package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

func TestTeamTaskSendsFinalFileInsteadOfGeneratedPreview(t *testing.T) {
	workspace := t.TempDir()
	ctx := tools.WithToolWorkspace(context.Background(), workspace)
	final := filepath.Join(workspace, "final.png")
	if err := os.WriteFile(final, []byte("final poster"), 0600); err != nil {
		t.Fatal(err)
	}
	loop := &Loop{}
	state := &runState{}
	request := &RunRequest{TeamTaskID: "task"}
	emit := func(AgentEvent) {}
	preview := "MEDIA:" + filepath.Join(workspace, "draft.png")
	loop.processToolResult(ctx, state, request, emit, providers.ToolCall{Name: "create_image"}, "create_image", tools.NewResult(preview), false)
	if len(state.mediaResults) != 0 {
		t.Fatalf("generated preview queued for delivery: %v", state.mediaResults)
	}
	args := map[string]any{"path": final}
	send := tools.NewSendFileTool(workspace, true)
	result := send.Execute(ctx, args)
	if result.IsError {
		t.Fatal(result.ForLLM)
	}
	loop.processToolResult(ctx, state, request, emit, providers.ToolCall{Name: send.Name(), Arguments: args}, send.Name(), result, false)
	if len(state.mediaResults) != 1 || state.mediaResults[0].Path != final {
		t.Fatalf("want final poster only, got %v", state.mediaResults)
	}
}
