package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/i18n"
	"github.com/nextlevelbuilder/goclaw/internal/media"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/security"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	usagecaps "github.com/nextlevelbuilder/goclaw/internal/usage/caps"
)

// --- Context helpers for media images ---

const ctxMediaImages toolContextKey = "tool_media_images"
const ctxMediaImageRefs toolContextKey = "tool_media_image_refs"

// WithMediaImages stores base64-encoded images in context for read_image tool access.
func WithMediaImages(ctx context.Context, images []providers.ImageContent) context.Context {
	return context.WithValue(ctx, ctxMediaImages, images)
}

// MediaImagesFromCtx retrieves stored images from context.
func MediaImagesFromCtx(ctx context.Context) []providers.ImageContent {
	v, _ := ctx.Value(ctxMediaImages).([]providers.ImageContent)
	return v
}

// WithMediaImageRefs stores image MediaRefs in chronological order for exact
// media_id resolution by image tools.
func WithMediaImageRefs(ctx context.Context, refs []providers.MediaRef) context.Context {
	return context.WithValue(ctx, ctxMediaImageRefs, refs)
}

// MediaImageRefsFromCtx retrieves image MediaRefs available to the current run.
func MediaImageRefsFromCtx(ctx context.Context) []providers.MediaRef {
	v, _ := ctx.Value(ctxMediaImageRefs).([]providers.MediaRef)
	return v
}

// imageMediaScope holds legacy storage access for one authorized conversation.
type imageMediaScope struct {
	sessionKey string
	store      *media.Store
}

const ctxImageMediaScope toolContextKey = "tool_image_media_scope"

// WithImageMediaAccess keeps the existing image-ref contract and adds scoped legacy access.
func WithImageMediaAccess(ctx context.Context, refs []providers.MediaRef, sessionKey string, mediaStore *media.Store) context.Context {
	ctx = WithMediaImageRefs(ctx, slices.Clone(refs))
	return context.WithValue(ctx, ctxImageMediaScope, imageMediaScope{sessionKey, mediaStore})
}

func (t *ReadImageTool) loadImageByID(ctx context.Context, id string) ([]providers.ImageContent, error) {
	ref, err := resolveImageMediaRef(ctx, id)
	if err == nil && ref.Kind == "image" {
		var imagePath string
		scope, _ := ctx.Value(ctxImageMediaScope).(imageMediaScope)
		switch {
		case ref.Path != "" && ToolWorkspaceFromCtx(ctx) != "":
			imagePath, err = resolveStructuredMediaRefPath(WithRestrictToWorkspace(ctx, true), ref.Path, "image")
		case ref.Path == "" && scope.store != nil && scope.sessionKey != "":
			imagePath, err = scope.store.LoadSessionPath(scope.sessionKey, ref.ID)
		}
		if err == nil && imagePath != "" {
			if images, loadErr := readImageFile(imagePath); loadErr == nil {
				return images, nil
			}
		}
	}
	return nil, errors.New(i18n.T(store.LocaleFromContext(ctx), i18n.MsgImageMediaUnavailable, id))
}

// --- ReadImageTool ---

// visionProviderPriority is the order in which providers are tried for vision.
// claude-cli follows anthropic so installations with a native Anthropic API key
// keep using the faster direct API, while claude-cli-only setups still resolve.
var visionProviderPriority = []string{"openrouter", "gemini", "anthropic", "claude-cli", "dashscope"}

// visionModelDefaults maps provider names to preferred vision models.
// Empty string lets the provider pick its own default model.
var visionModelDefaults = map[string]string{
	"openrouter": "google/gemini-2.5-flash-image",
	"gemini":     "gemini-2.5-flash",
	"anthropic":  "",
	"claude-cli": "",
	"dashscope":  "qwen3-vl",
}

// ReadImageTool uses a vision-capable provider to describe images attached to the current message.
type ReadImageTool struct {
	registry  *providers.Registry
	usageCaps *usagecaps.Service
}

func NewReadImageTool(registry *providers.Registry) *ReadImageTool {
	return &ReadImageTool{registry: registry}
}

func (t *ReadImageTool) SetUsageCapService(svc *usagecaps.Service) {
	t.usageCaps = svc
}

func (t *ReadImageTool) Name() string { return "read_image" }

func (t *ReadImageTool) Description() string {
	return "Analyze images using vision AI. Works with images sent by the user, workspace/generated image files, or public HTTP/HTTPS image URLs."
}

func (t *ReadImageTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "What you want to know about the image(s). E.g. 'Describe this image in detail' or 'What text is in this image?'",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Optional logical file path to an image in the workspace (for example .uploads/photo.jpg or inputs/photo.jpg). Never reconstruct an absolute workspace path.",
			},
			"media_id": map[string]any{
				"type":        "string",
				"description": "Optional exact media ID from a <media:image id=\"...\"> tag for in-process tool execution. Takes priority over path. Claude CLI/MCP callers should use the tag's logical path.",
			},
			"url": map[string]any{
				"type":        "string",
				"description": "Optional URL to an image. Use this to analyze images hosted online.",
			},
		},
		"required": []string{"prompt"},
	}
}

// maxImageFileBytes is the max size for loading workspace images (10MB).
const maxImageFileBytes = 10 * 1024 * 1024

func (t *ReadImageTool) Execute(ctx context.Context, args map[string]any) *Result {
	prompt, _ := args["prompt"].(string)
	if prompt == "" {
		prompt = "Describe this image in detail."
	}

	imgPath, _ := args["path"].(string)
	imgURL, _ := args["url"].(string)
	mediaID, validMediaID := args["media_id"].(string)
	if _, provided := args["media_id"]; provided && (!validMediaID || strings.TrimSpace(mediaID) == "") {
		return ErrorResult(i18n.T(store.LocaleFromContext(ctx), i18n.MsgImageMediaIDRequired))
	}

	if imgPath != "" && imgURL != "" && mediaID == "" {
		return ErrorResult("Both 'path' and 'url' parameters cannot be specified. Choose only one.")
	}
	if mediaID != "" && imgURL != "" {
		return ErrorResult("Only one of 'url' or 'media_id' may be specified.")
	}
	// <media:image> tags expose both id and path, so models often echo both.
	// The ID is the exact reference; the path adds nothing and must not fail the call.
	if mediaID != "" {
		imgPath = ""
	}

	// If path is provided, load image from workspace file
	images := MediaImagesFromCtx(ctx)
	if imgPath != "" {
		fileImages, err := t.loadImageFromPath(ctx, imgPath)
		if err != nil {
			return ErrorResult(err.Error())
		}
		images = fileImages
	} else if mediaID != "" {
		fileImages, err := t.loadImageByID(ctx, mediaID)
		if err != nil {
			return ErrorResult(err.Error())
		}
		images = fileImages
	} else if imgURL != "" {
		if _, _, err := security.Validate(imgURL); err != nil {
			return ErrorResult(fmt.Sprintf("Invalid image URL: %v", err))
		}
		images = []providers.ImageContent{{
			URL: imgURL,
		}}
	}

	if len(images) == 0 {
		return ErrorResult("No images available. Either send an image in the chat, provide a file path with 'path', or provide an image URL with 'url'.")
	}

	chain := ResolveMediaProviderChain(ctx, "read_image", "", "",
		visionProviderPriority, visionModelDefaults, t.registry)

	// Inject prompt and images into each chain entry's params
	for i := range chain {
		if chain[i].Params == nil {
			chain[i].Params = make(map[string]any)
		}
		chain[i].Params["prompt"] = prompt
		chain[i].Params["images"] = images
	}

	if len(chain) == 0 {
		return ErrorResult("No vision provider configured. Ask the user to add a vision-capable provider (e.g. Gemini, Anthropic, OpenRouter) in the system settings.")
	}

	chainResult, err := ExecuteWithChain(ctx, chain, t.registry, t.callProvider)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Image analysis failed — all vision providers returned errors: %v. The user may need to check their provider API keys or configuration.", err))
	}

	result := NewResult(string(chainResult.Data))
	result.Usage = chainResult.Usage
	result.Provider = chainResult.Provider
	result.Model = chainResult.Model
	return result
}

func resolveImageMediaRef(ctx context.Context, mediaID string) (providers.MediaRef, error) {
	refs := MediaImageRefsFromCtx(ctx)
	if len(refs) == 0 {
		return providers.MediaRef{}, fmt.Errorf("no image media references available in this conversation")
	}
	if mediaID == "latest" {
		return refs[len(refs)-1], nil
	}
	// Newest first: assistant file IDs are base names, so a revised file can reuse
	// the ID of an older one and must win over it.
	for _, ref := range slices.Backward(refs) {
		if ref.ID == mediaID {
			return ref, nil
		}
	}
	return providers.MediaRef{}, fmt.Errorf("image media_id %q not found in this conversation", mediaID)
}

// callProvider dispatches the vision call using provider.Chat().
func (t *ReadImageTool) callProvider(ctx context.Context, cp credentialProvider, providerName, model string, params map[string]any) ([]byte, *providers.Usage, error) {
	prompt := GetParamString(params, "prompt", "Describe this image in detail.")
	images, _ := params["images"].([]providers.ImageContent)

	for _, img := range images {
		if img.URL == "" {
			continue
		}
		if _, _, err := security.Validate(img.URL); err != nil {
			return nil, nil, fmt.Errorf("invalid image URL: %w", err)
		}
	}

	// Anthropic Claude does not support URL references and requires base64-encoded image data.
	if providerName == "anthropic" || providerName == "claude-cli" {
		for _, img := range images {
			if img.URL != "" && img.Data == "" {
				return nil, nil, fmt.Errorf("provider %q does not support analyzing images directly from a URL", providerName)
			}
		}
	}

	// Get the full provider for Chat() access
	p, err := t.registry.Get(ctx, providerName)
	if err != nil {
		return nil, nil, fmt.Errorf("provider %q not available: %w", providerName, err)
	}

	slog.Info("read_image: calling vision provider", "provider", providerName, "model", model, "images", len(images))

	opts := map[string]any{
		"max_tokens":  1024,
		"temperature": 0.3,
	}
	// claude-cli spawns the Claude CLI binary; loading its built-in MCP
	// toolset costs latency we don't need for a one-shot vision call. Keep
	// this flag scoped to claude-cli so other providers don't receive
	// options they ignore (or worse, choke on in the future).
	if providerName == "claude-cli" {
		opts["disable_tools"] = true
	}

	chatReq := providers.ChatRequest{
		Messages: []providers.Message{
			{
				Role:    "user",
				Content: prompt,
				Images:  images,
			},
		},
		Model:   model,
		Options: opts,
	}
	reservation, reserveErr := reserveToolLLMUsage(ctx, t.usageCaps, t.Name(), providerName, model, chatReq)
	if reserveErr != nil {
		return nil, nil, reserveErr
	}
	resp, err := p.Chat(ctx, chatReq)
	if reservation != nil {
		reservation.Reconcile(ctx, resp, err)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("vision provider error: %w", err)
	}

	return []byte(resp.Content), resp.Usage, nil
}

// loadImageFromPath reads an image file from the workspace and returns it as ImageContent.
func (t *ReadImageTool) loadImageFromPath(ctx context.Context, path string) ([]providers.ImageContent, error) {
	resolved, err := resolveStructuredMediaPath(ctx, path, "image")
	if err != nil {
		return nil, err
	}

	return readImageFile(resolved)
}

func readImageFile(path string) ([]providers.ImageContent, error) {
	if err := ValidateRegularFileForRead(path); err != nil {
		return nil, fmt.Errorf("image is not a safe regular file: %w", err)
	}
	// Infer MIME type from extension
	ext := strings.ToLower(filepath.Ext(path))
	mimeTypes := map[string]string{
		".jpg": "image/jpeg", ".jpeg": "image/jpeg",
		".png": "image/png", ".gif": "image/gif",
		".webp": "image/webp", ".bmp": "image/bmp",
	}
	mime, ok := mimeTypes[ext]
	if !ok {
		return nil, fmt.Errorf("unsupported image format: %s (supported: jpg, png, gif, webp, bmp)", ext)
	}

	// Pre-check file size before loading into memory.
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("failed to stat image file: %w", err)
	}
	if fi.Size() > maxImageFileBytes {
		return nil, fmt.Errorf("image file too large (%d bytes, max %d)", fi.Size(), maxImageFileBytes)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read image file: %w", err)
	}

	return []providers.ImageContent{{
		MimeType: mime,
		Data:     base64.StdEncoding.EncodeToString(data),
	}}, nil
}
