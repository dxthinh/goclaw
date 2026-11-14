package providers

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// NativeImageProvider is implemented by OAuth-backed providers whose upstream
// exposes an image_generation native tool (ChatGPT Responses API style).
// create_image routes through this interface when the chain resolves to such
// a provider, bypassing the credentialProvider (APIKey/APIBase) path.
type NativeImageProvider interface {
	GenerateImage(ctx context.Context, req NativeImageRequest) (*NativeImageResult, error)
}

// DefaultImageModel is the image model used by the Responses API image_generation
// tool when the caller does not specify one. GPT Image 2.5 (2026-09-08) ships as
// two model IDs instead of one: "flare" tuned for speed, "sunburst" tuned for
// editing precision. Flare is the default because it beats gpt-image-2 on quality
// at roughly half the latency, which suits the one-shot create_image path.
const DefaultImageModel = "gpt-image-2.5-flare"

// ImageModelInfo is one image model offered for the native image_generation tool.
type ImageModelInfo struct {
	ID   string
	Name string
}

// nativeImageModels is the curated catalog of image models the ChatGPT Responses
// API image_generation tool accepts, default first. The ChatGPT backend exposes no
// image-model listing endpoint (GET /codex/models only returns chat models), so
// this catalog is the single source the web UI reads via the provider models API.
//
// GPT Image 2.6 (2026-11-04) được liệt kê để chọn thủ công nhưng chưa làm mặc định:
// tài liệu công khai chỉ xác nhận 2.6 trên Image API, chưa xác nhận trên tool
// image_generation của Responses API mà đường ChatGPT OAuth này dùng.
var nativeImageModels = []ImageModelInfo{
	{ID: "gpt-image-2.5-flare", Name: "GPT Image 2.5 Flare"},       // default - fastest at high quality
	{ID: "gpt-image-2.5-sunburst", Name: "GPT Image 2.5 Sunburst"}, // most capable at editing precision
	{ID: "gpt-image-2.6-flare", Name: "GPT Image 2.6 Flare"},       // newest, fast iteration
	{ID: "gpt-image-2.6-sunburst", Name: "GPT Image 2.6 Sunburst"}, // newest, detailed work
	{ID: "gpt-image-2", Name: "GPT Image 2"},                       // previous generation
	{ID: "gpt-image-1.5", Name: "GPT Image 1.5"},                   // legacy fallback
}

// gptImageModelPattern matches the OpenAI GPT Image model family ("gpt-image-*",
// "chatgpt-image-*"). Matching the family instead of a fixed whitelist lets a newly
// released model work without a code change, while still rejecting unrelated
// names such as "dall-e-3" before they reach upstream.
var gptImageModelPattern = regexp.MustCompile(`^(gpt-image|chatgpt-image)-[a-z0-9][a-z0-9.-]*$`)

// NativeImageModels returns a copy of the curated image model catalog.
func NativeImageModels() []ImageModelInfo {
	return append([]ImageModelInfo(nil), nativeImageModels...)
}

// IsGPTImageModel reports whether model belongs to the GPT Image family.
func IsGPTImageModel(model string) bool {
	return gptImageModelPattern.MatchString(model)
}

// ValidateImageModel returns the model to use, or an error if the caller
// supplied a value outside the GPT Image family. Empty input returns DefaultImageModel.
func ValidateImageModel(model string) (string, error) {
	if model == "" {
		return DefaultImageModel, nil
	}
	if !IsGPTImageModel(model) {
		ids := make([]string, 0, len(nativeImageModels))
		for _, m := range nativeImageModels {
			ids = append(ids, m.ID)
		}
		return "", fmt.Errorf("unsupported image model %q; use a gpt-image-* model such as: %s (default %s)",
			model, strings.Join(ids, ", "), DefaultImageModel)
	}
	return model, nil
}

// NativeImageRequest describes a single image generation request.
type NativeImageRequest struct {
	// Model is the parent LLM model for the Responses API call (e.g. "gpt-5.5").
	// NOT the image model — see ImageModel below.
	// If empty, the provider uses its own default LLM model.
	Model string

	// ImageModel is the image-generation model attached to the image_generation
	// tool (e.g. "gpt-image-2.5-flare"). Must be a value accepted by ValidateImageModel;
	// empty falls back to DefaultImageModel.
	ImageModel string

	// Prompt is the text description of the image.
	Prompt string

	// AspectRatio is the desired aspect ratio, e.g. "16:9", "1:1", "9:16".
	// Converted to a concrete pixel size by the provider implementation.
	// Defaults to "1:1" if empty.
	AspectRatio string

	// OutputFormat is the desired image format: "png" (default), "jpg", "webp".
	OutputFormat string

	// RefImages contains the list of reference images.
	RefImages []RefImage
}

// RefImage represents a single reference image for image-to-image or styling tasks.
type RefImage struct {
	Data     []byte
	Base64   string
	MimeType string
	URL      string
	Strength float64
}

// NativeImageResult holds the result of a native image generation call.
type NativeImageResult struct {
	// MimeType is the detected MIME type of the generated image (e.g. "image/png").
	MimeType string

	// Data is the raw decoded image bytes (NOT base64).
	Data []byte

	// Usage is optional token usage if the provider reports it.
	Usage *Usage
}

// SizeFromAspect converts a common aspect ratio string to a pixel dimension
// string expected by image generation APIs (e.g. "1792x1024").
// Falls back to "1024x1024" for unrecognised ratios.
func SizeFromAspect(aspectRatio string) string {
	switch aspectRatio {
	case "16:9":
		return "1792x1024"
	case "9:16":
		return "1024x1792"
	case "3:4":
		return "1024x1365"
	case "4:3":
		return "1365x1024"
	default:
		return "1024x1024"
	}
}
