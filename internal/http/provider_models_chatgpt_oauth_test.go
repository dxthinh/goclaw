package http

import (
	"net/http"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/oauth"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestCodexReasoningCapabilityPrefersCuratedTable(t *testing.T) {
	got := codexReasoningCapability(oauth.CodexModel{
		Slug:                     "gpt-5.5",
		SupportedReasoningLevels: []oauth.CodexReasoningOption{{Effort: "low"}},
	})
	if got == nil || !slices.Contains(got.Levels, "none") || got.DefaultEffort != "medium" {
		t.Fatalf("capability = %+v, want curated gpt-5.5 entry", got)
	}
}

func TestCodexReasoningCapabilityDerivesFromCatalog(t *testing.T) {
	got := codexReasoningCapability(oauth.CodexModel{
		Slug:                  "gpt-6.1-sol",
		DefaultReasoningLevel: "low",
		SupportedReasoningLevels: []oauth.CodexReasoningOption{
			{Effort: "low"}, {Effort: "medium"}, {Effort: "xhigh"}, {Effort: "max"}, {Effort: "ultra"},
		},
	})
	if got == nil {
		t.Fatal("capability = nil, want derived levels")
	}
	if want := []string{"low", "medium", "xhigh"}; !slices.Equal(got.Levels, want) {
		t.Fatalf("levels = %v, want %v (unknown efforts dropped)", got.Levels, want)
	}
	if got.DefaultEffort != "low" {
		t.Fatalf("default_effort = %q, want low", got.DefaultEffort)
	}
}

func TestCodexReasoningCapabilityNilWithoutUsableLevels(t *testing.T) {
	got := codexReasoningCapability(oauth.CodexModel{
		Slug:                     "gpt-unknown",
		SupportedReasoningLevels: []oauth.CodexReasoningOption{{Effort: "ultra"}},
	})
	if got != nil {
		t.Fatalf("capability = %+v, want nil", got)
	}
}

func TestProvidersHandlerListProviderModelsChatGPTOAuthIncludesImageModels(t *testing.T) {
	token := setupProvidersAdminToken(t)
	providerStore := newMockProviderStore()
	provider := &store.LLMProviderData{
		BaseModel:    store.BaseModel{ID: uuid.New()},
		Name:         "openai-codex",
		ProviderType: store.ProviderChatGPTOAuth,
		Enabled:      true,
	}
	if err := providerStore.CreateProvider(t.Context(), provider); err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}

	handler := NewProvidersHandler(providerStore, newMockSecretsStore(), nil, "")
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	result := providerModelsRequest(t, mux, provider.ID, token)
	if result.DefaultImageModel != providers.DefaultImageModel {
		t.Fatalf("default_image_model = %q, want %q", result.DefaultImageModel, providers.DefaultImageModel)
	}
	catalog := providers.NativeImageModels()
	if len(result.ImageModels) != len(catalog) {
		t.Fatalf("image_models = %+v, want %d catalog entries", result.ImageModels, len(catalog))
	}
	for i, m := range catalog {
		if result.ImageModels[i].ID != m.ID || result.ImageModels[i].Name != m.Name {
			t.Fatalf("image_models[%d] = %+v, want %+v", i, result.ImageModels[i], m)
		}
	}
}
