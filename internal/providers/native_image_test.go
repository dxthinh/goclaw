package providers

import (
	"strings"
	"testing"
)

func TestValidateImageModel(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "empty falls back to default", input: "", want: DefaultImageModel},
		{name: "flare", input: "gpt-image-2.5-flare", want: "gpt-image-2.5-flare"},
		{name: "sunburst", input: "gpt-image-2.5-sunburst", want: "gpt-image-2.5-sunburst"},
		{name: "2.6 flare", input: "gpt-image-2.6-flare", want: "gpt-image-2.6-flare"},
		{name: "2.6 sunburst", input: "gpt-image-2.6-sunburst", want: "gpt-image-2.6-sunburst"},
		{name: "previous generation", input: "gpt-image-2", want: "gpt-image-2"},
		{name: "1.5 still served upstream", input: "gpt-image-1.5", want: "gpt-image-1.5"},
		{name: "1-mini still served upstream", input: "gpt-image-1-mini", want: "gpt-image-1-mini"},
		{name: "retired gpt-image-1", input: "gpt-image-1", wantErr: true},
		{name: "future family member passes through", input: "gpt-image-3", want: "gpt-image-3"},
		{name: "chatgpt-image alias", input: "chatgpt-image-latest", want: "chatgpt-image-latest"},
		{name: "unrelated model", input: "dall-e-3", wantErr: true},
		{name: "bare family prefix", input: "gpt-image-", wantErr: true},
		{name: "uppercase is not a model id", input: "GPT-IMAGE-2", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateImageModel(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateImageModel(%q) = %q, want error", tc.input, got)
				}
				// The error must list the allowed models so the caller can self-correct.
				if !strings.Contains(err.Error(), DefaultImageModel) {
					t.Errorf("error %q does not mention the default model %q", err, DefaultImageModel)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateImageModel(%q) returned error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("ValidateImageModel(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestDefaultImageModelLeadsCatalog guards the catalog the web UI renders: the
// default must be listed first and every entry must pass ValidateImageModel,
// otherwise the dropdown would offer a model that create_image rejects.
func TestDefaultImageModelLeadsCatalog(t *testing.T) {
	models := NativeImageModels()
	if len(models) == 0 || models[0].ID != DefaultImageModel {
		t.Fatalf("NativeImageModels()[0] = %+v, want %q first", models, DefaultImageModel)
	}
	for _, m := range models {
		if _, err := ValidateImageModel(m.ID); err != nil {
			t.Errorf("catalog model %q rejected: %v", m.ID, err)
		}
		if m.Name == "" {
			t.Errorf("catalog model %q has no display name", m.ID)
		}
	}
}

// GPT Image 2.6 phải có trong catalog để UI cho chọn, và được gửi nguyên ID lên upstream.
func TestNativeImageCatalogOffersGPTImage26(t *testing.T) {
	ids := map[string]bool{}
	for _, m := range NativeImageModels() {
		ids[m.ID] = true
	}
	for _, want := range []string{"gpt-image-2.6-flare", "gpt-image-2.6-sunburst"} {
		if !ids[want] {
			t.Errorf("catalog thiếu %q", want)
		}
	}

	p := NewCodexProvider("codex-test", &staticTokenSource{token: "tok"}, "http://unused", "gpt-test")
	body := p.buildNativeImageRequestBody("gpt-test", NativeImageRequest{ImageModel: "gpt-image-2.6-sunburst", Prompt: "x", OutputFormat: "png"})
	tool := body["tools"].([]map[string]any)[0]
	if tool["model"] != "gpt-image-2.6-sunburst" {
		t.Errorf("tools[0].model = %v, muốn gpt-image-2.6-sunburst", tool["model"])
	}
}
