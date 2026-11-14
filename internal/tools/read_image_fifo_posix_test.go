//go:build linux || darwin

package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"golang.org/x/sys/unix"
)

func TestReadImageRejectsFIFO(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "blocked.png")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatalf("create FIFO: %v", err)
	}
	tool := NewReadImageTool(nil)
	ctx := WithToolWorkspace(context.Background(), workspace)
	// No writer is opened: reading the FIFO would block until the test timeout.
	t.Run("path", func(t *testing.T) {
		if _, err := tool.loadImageFromPath(ctx, "blocked.png"); err == nil {
			t.Fatal("FIFO path was accepted")
		}
	})
	t.Run("media ID", func(t *testing.T) {
		refCtx := WithImageMediaAccess(ctx, []providers.MediaRef{{ID: "pipe", Kind: "image", Path: path}}, "session", nil)
		if _, err := tool.loadImageByID(refCtx, "pipe"); err == nil {
			t.Fatal("FIFO media reference was accepted")
		}
	})
	t.Run("file reader", func(t *testing.T) {
		if _, err := readImageFile(path); err == nil {
			t.Fatal("FIFO was accepted by the shared image reader")
		}
	})
}
