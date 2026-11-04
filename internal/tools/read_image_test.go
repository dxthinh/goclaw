package tools

import (
	"context"
	"strings"
	"testing"

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
