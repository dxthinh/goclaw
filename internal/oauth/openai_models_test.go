package oauth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type stubTokenSource struct {
	token string
	err   error
}

func (s stubTokenSource) Token() (string, error) { return s.token, s.err }

func TestFetchCodexModelsFiltersHiddenAndSortsByPriority(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/models" {
			t.Fatalf("path = %q, want /backend-api/codex/models", r.URL.Path)
		}
		if got := r.URL.Query().Get("client_version"); got != codexModelsClientVersion {
			t.Fatalf("client_version = %q, want %q", got, codexModelsClientVersion)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "acct_123" {
			t.Fatalf("ChatGPT-Account-Id = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[
			{"slug":"gpt-6-sol","display_name":"GPT-6-Sol","visibility":"list","priority":3},
			{"slug":"codex-auto-review","display_name":"Codex Auto Review","visibility":"hide","priority":0},
			{"slug":"gpt-6.1-sol","display_name":"GPT-6.1-Sol","visibility":"list","priority":1,
			 "default_reasoning_level":"low",
			 "supported_reasoning_levels":[{"effort":"low"},{"effort":"high"},{"effort":"ultra"}]},
			{"slug":"","display_name":"Broken","visibility":"list","priority":2}
		]}`))
	}))
	defer server.Close()

	provider := &store.LLMProviderData{
		APIBase:  server.URL + "/backend-api/",
		Settings: json.RawMessage(`{"account_id":"acct_123"}`),
	}
	models, err := FetchCodexModels(t.Context(), provider, stubTokenSource{token: "access-token"})
	if err != nil {
		t.Fatalf("FetchCodexModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2 listed models: %+v", len(models), models)
	}
	if models[0].Slug != "gpt-6.1-sol" || models[1].Slug != "gpt-6-sol" {
		t.Fatalf("order = [%s %s], want priority order [gpt-6.1-sol gpt-6-sol]", models[0].Slug, models[1].Slug)
	}
	if got := models[0].SupportedReasoningLevels; len(got) != 3 || got[2].Effort != "ultra" {
		t.Fatalf("supported_reasoning_levels = %+v, want raw catalog levels", got)
	}
}

func TestFetchCodexModelsOmitsAccountHeaderWhenUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, ok := r.Header["Chatgpt-Account-Id"]; ok {
			t.Fatalf("ChatGPT-Account-Id = %q, want header omitted", got)
		}
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer server.Close()

	models, err := FetchCodexModels(t.Context(), &store.LLMProviderData{APIBase: server.URL}, stubTokenSource{token: "tok"})
	if err != nil {
		t.Fatalf("FetchCodexModels() error = %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("models = %+v, want empty", models)
	}
}

func TestFetchCodexModelsReturnsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"unauthorized"}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := FetchCodexModels(t.Context(), &store.LLMProviderData{APIBase: server.URL}, stubTokenSource{token: "tok"})
	var httpErr *providers.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusUnauthorized {
		t.Fatalf("error = %v, want HTTPError 401", err)
	}
}

func TestFetchCodexModelsRetriesTransientFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-6.1-sol","visibility":"list"}]}`))
	}))
	defer server.Close()

	models, err := FetchCodexModels(t.Context(), &store.LLMProviderData{APIBase: server.URL}, stubTokenSource{token: "tok"})
	if err != nil {
		t.Fatalf("FetchCodexModels() error = %v", err)
	}
	if calls != 2 || len(models) != 1 {
		t.Fatalf("calls = %d, models = %+v; want one retry then the catalog", calls, models)
	}
}

func TestFetchCodexModelsFailsWithoutToken(t *testing.T) {
	_, err := FetchCodexModels(t.Context(), &store.LLMProviderData{}, stubTokenSource{err: errors.New("no token")})
	if err == nil {
		t.Fatal("FetchCodexModels() error = nil, want token error")
	}
}
