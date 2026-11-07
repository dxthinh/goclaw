package tools

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/media"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

func TestReadImage_BothPathAndUrl_Error(t *testing.T) {
	tool := NewReadImageTool(nil)

	res := tool.Execute(context.Background(), map[string]any{
		"prompt": "describe this",
		"path":   "workspace/image.png",
		"url":    "https://example.com/image.png",
	})

	if !res.IsError {
		t.Fatalf("expected error when both path and url are provided")
	}

	if !strings.Contains(res.ForLLM, "Both 'path' and 'url' parameters cannot be specified") {
		t.Errorf("unexpected error message: %s", res.ForLLM)
	}
}

func TestReadImage_PrivateURL_Error(t *testing.T) {
	tool := NewReadImageTool(nil)

	res := tool.Execute(context.Background(), map[string]any{
		"prompt": "describe this",
		"url":    "http://127.0.0.1/image.png",
	})

	if !res.IsError {
		t.Fatalf("expected error for private image URL")
	}
	if !strings.Contains(res.ForLLM, "Invalid image URL") {
		t.Errorf("unexpected error message: %s", res.ForLLM)
	}
}

func TestReadImageDelegationInputResolution(t *testing.T) {
	inputs, outputs := t.TempDir(), t.TempDir()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aX1sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	inputPath := filepath.Join(inputs, "photo.png")
	if err := os.WriteFile(inputPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	// A matching output path must never shadow the captured delegation input.
	if err := os.Mkdir(filepath.Join(outputs, "inputs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputs, "inputs", "photo.png"), []byte("wrong output"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := WithDelegationArtifactInputs(WithToolWorkspace(context.Background(), outputs), inputs)
	tool := NewReadImageTool(nil)
	t.Run("logical path", func(t *testing.T) {
		images, err := tool.loadImageFromPath(ctx, "inputs/photo.png")
		if err != nil || len(images) != 1 || images[0].Data != base64.StdEncoding.EncodeToString(data) {
			t.Fatalf("staged image = %+v, error = %v", images, err)
		}
	})
	for _, path := range []string{inputPath, "inputs/photo.png"} {
		t.Run("media ref "+path, func(t *testing.T) {
			refCtx := WithImageMediaAccess(ctx, []providers.MediaRef{{ID: "staged", Kind: "image", Path: path}}, "delegation", nil)
			images, err := tool.loadImageByID(refCtx, "staged")
			if err != nil || len(images) != 1 || images[0].Data != base64.StdEncoding.EncodeToString(data) {
				t.Fatalf("staged media ref = %+v, error = %v", images, err)
			}
		})
	}
}

func TestResolveImageMediaRefRequiresExactID(t *testing.T) {
	ctx := WithMediaImageRefs(context.Background(), []providers.MediaRef{
		{ID: "first-id", Kind: "image", Path: ".uploads/first.png"},
		{ID: "second-id", Kind: "image", Path: ".uploads/second.png"},
	})

	got, err := resolveImageMediaRef(ctx, "first-id")
	if err != nil || got.ID != "first-id" {
		t.Fatalf("exact media ID = %#v, %v", got, err)
	}
	latest, err := resolveImageMediaRef(ctx, "latest")
	if err != nil || latest.ID != "second-id" {
		t.Fatalf("latest media ID = %#v, %v", latest, err)
	}
	if _, err := resolveImageMediaRef(ctx, "missing-id"); err == nil {
		t.Fatal("unknown media ID unexpectedly resolved")
	}
}

func TestResolveImageMediaRefPrefersNewestDuplicateID(t *testing.T) {
	ctx := WithMediaImageRefs(context.Background(), []providers.MediaRef{
		{ID: "poster.png", Kind: "image", Path: "old/poster.png"},
		{ID: "poster.png", Kind: "image", Path: "new/poster.png"},
	})

	got, err := resolveImageMediaRef(ctx, "poster.png")
	if err != nil || got.Path != "new/poster.png" {
		t.Fatalf("duplicate media ID = %#v, %v", got, err)
	}
}

func TestReadImage_MediaIDWithPath_UsesMediaID(t *testing.T) {
	tool := NewReadImageTool(nil)
	ctx := WithMediaImageRefs(context.Background(), []providers.MediaRef{
		{ID: "real-id", Kind: "image", Path: "missing/from-ref.png"},
	})

	res := tool.Execute(ctx, map[string]any{
		"prompt":   "describe this",
		"media_id": "real-id",
		"path":     "ignored/from-path.png",
	})

	if !res.IsError {
		t.Fatal("expected the missing ref file to fail")
	}
	if strings.Contains(res.ForLLM, "Only one of") || strings.Contains(res.ForLLM, "from-path") {
		t.Fatalf("path was not ignored: %s", res.ForLLM)
	}
}

func TestReadImage_MediaIDWithURL_Error(t *testing.T) {
	tool := NewReadImageTool(nil)

	res := tool.Execute(context.Background(), map[string]any{
		"prompt":   "describe this",
		"media_id": "real-id",
		"url":      "https://example.com/image.png",
	})

	if !res.IsError || !strings.Contains(res.ForLLM, "Only one of") {
		t.Fatalf("expected conflict error, got: %s", res.ForLLM)
	}
}

func TestReadImage_AnthropicURL_Error(t *testing.T) {
	tool := NewReadImageTool(nil)

	params := map[string]any{
		"prompt": "describe this",
		"images": []providers.ImageContent{
			{
				URL: "https://93.184.216.34/image.png",
			},
		},
	}

	_, _, err := tool.callProvider(context.Background(), nil, "anthropic", "claude-3-sonnet", params)
	if err == nil {
		t.Fatalf("expected error for anthropic provider with image URL")
	}

	if !strings.Contains(err.Error(), "does not support analyzing images directly from a URL") {
		t.Errorf("unexpected error message: %v", err)
	}

	// Should also error for claude-cli
	_, _, err = tool.callProvider(context.Background(), nil, "claude-cli", "claude-3-sonnet", params)
	if err == nil {
		t.Fatalf("expected error for claude-cli provider with image URL")
	}
}

func TestReadImageMediaIDResolution(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "upload.png")
	data := []byte("uploaded image bytes")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	ref := providers.MediaRef{ID: "current-image", Kind: "image", Path: path, MimeType: "image/png"}
	tool := NewReadImageTool(nil)
	ctx := WithToolWorkspace(context.Background(), workspace)
	ctx = WithImageMediaAccess(ctx, []providers.MediaRef{ref}, "user-a/session-a", nil)
	images, err := tool.loadImageByID(ctx, ref.ID)
	if err != nil || len(images) != 1 || images[0].Data != base64.StdEncoding.EncodeToString(data) {
		t.Fatalf("resolved images = %+v, error = %v", images, err)
	}

	for _, id := range []string{"missing", path, "../upload.png", "<media:image>", "*"} {
		t.Run(id, func(t *testing.T) {
			// Explicit IDs never fall back to current-turn base64 or a filesystem path.
			withFallback := WithMediaImages(ctx, images)
			result := tool.Execute(withFallback, map[string]any{"media_id": id})
			if !result.IsError || !strings.Contains(result.ForLLM, "unavailable in this conversation") {
				t.Fatalf("unexpected result: %+v", result)
			}
		})
	}

	otherUser := WithToolWorkspace(ctx, t.TempDir())
	otherUser = WithRestrictToWorkspace(otherUser, false)
	if _, err := tool.loadImageByID(otherUser, ref.ID); err == nil {
		t.Fatal("cross-user workspace image was accessible")
	}
	otherSession := WithImageMediaAccess(ctx, nil, "user-a/session-b", nil)
	if _, err := tool.loadImageByID(otherSession, ref.ID); err == nil {
		t.Fatal("cross-session image was accessible")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.loadImageByID(ctx, ref.ID); err == nil {
		t.Fatal("deleted image was accessible")
	}
}

func TestReadImageMediaIDLegacyStore(t *testing.T) {
	store, err := media.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(src, []byte("legacy image"), 0600); err != nil {
		t.Fatal(err)
	}
	id, _, err := store.SaveFile("user-a/session-a", src, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	refs := []providers.MediaRef{{ID: id, Kind: "image", MimeType: "image/png"}}
	tool := NewReadImageTool(nil)
	ctx := WithImageMediaAccess(context.Background(), refs, "user-a/session-a", store)
	images, err := tool.loadImageByID(ctx, id)
	if err != nil || len(images) != 1 || images[0].Data != base64.StdEncoding.EncodeToString([]byte("legacy image")) {
		t.Fatalf("legacy images = %+v, error = %v", images, err)
	}
	withoutRefs := WithImageMediaAccess(context.Background(), nil, "user-a/session-a", store)
	if _, err := tool.loadImageByID(withoutRefs, id); err == nil {
		t.Fatal("store ID without a conversation reference was accessible")
	}
	wrongKind := WithImageMediaAccess(context.Background(), []providers.MediaRef{{ID: id, Kind: "document"}}, "user-a/session-a", store)
	if _, err := tool.loadImageByID(wrongKind, id); err == nil {
		t.Fatal("non-image reference was accessible")
	}
	for _, session := range []string{"user-a/session-b", "user-b/session-a", ""} {
		ctx := WithImageMediaAccess(context.Background(), refs, session, store)
		if _, err := tool.loadImageByID(ctx, id); err == nil {
			t.Fatalf("legacy image accessible from %q", session)
		}
	}
}

func TestReadImageMediaIDConflictingInputs(t *testing.T) {
	tool := NewReadImageTool(nil)
	result := tool.Execute(context.Background(), map[string]any{"media_id": "id", "url": "https://example.com/image.png"})
	if !result.IsError || !strings.Contains(result.ForLLM, "Only one") {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestReadImageWorkspacePathIsolation(t *testing.T) {
	tool := NewReadImageTool(nil)
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("private image"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := WithToolWorkspace(context.Background(), workspace)
	if _, err := tool.loadImageFromPath(ctx, outside); err == nil {
		t.Fatal("outside workspace path was accessible")
	}
	link := filepath.Join(workspace, "link.png")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	ctx = WithImageMediaAccess(ctx, []providers.MediaRef{{ID: "linked", Kind: "image", Path: link}}, "session", nil)
	if _, err := tool.loadImageByID(ctx, "linked"); err == nil {
		t.Fatal("symlink to another user's image was accessible")
	}
	inside := filepath.Join(workspace, "inside.png")
	if err := os.WriteFile(inside, []byte("workspace image"), 0600); err != nil {
		t.Fatal(err)
	}
	if images, err := tool.loadImageFromPath(ctx, inside); err != nil || len(images) != 1 {
		t.Fatalf("workspace path failed: images=%+v error=%v", images, err)
	}
}

func TestReadImageMediaIDInvalidInput(t *testing.T) {
	tool := NewReadImageTool(nil)
	ctx := WithMediaImages(context.Background(), []providers.ImageContent{{Data: "current image"}})
	for _, id := range []any{nil, 123, "", " "} {
		result := tool.Execute(ctx, map[string]any{"media_id": id})
		if !result.IsError || !strings.Contains(result.ForLLM, "must be a non-empty image ID") {
			t.Fatalf("invalid ID fell back to current images: %+v", result)
		}
	}
}
