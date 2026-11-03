package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	// codexModelsClientVersion is sent as client_version on the Codex model
	// catalog request. The backend hides models whose minimal_client_version is
	// newer than this, so an old value shrinks the list and an empty list falls
	// back to the static catalog in the caller.
	codexModelsClientVersion = "0.166.0"
	codexModelsUserAgent     = "codex_cli_rs/" + codexModelsClientVersion
	codexModelsTimeout       = 10 * time.Second
	// codexModelsMaxBytes mirrors the Codex CLI limit for catalog responses.
	codexModelsMaxBytes = 1 << 20
)

var codexModelsHTTPClient = &http.Client{Timeout: codexModelsTimeout}

// CodexModel is one entry of the ChatGPT backend model catalog
// (GET {apiBase}/codex/models), trimmed to the fields GoClaw consumes.
type CodexModel struct {
	Slug                     string                 `json:"slug"`
	DisplayName              string                 `json:"display_name"`
	Visibility               string                 `json:"visibility"`
	Priority                 int                    `json:"priority"`
	DefaultReasoningLevel    string                 `json:"default_reasoning_level"`
	SupportedReasoningLevels []CodexReasoningOption `json:"supported_reasoning_levels"`
}

// CodexReasoningOption is one reasoning effort a catalog model accepts.
type CodexReasoningOption struct {
	Effort string `json:"effort"`
}

// FetchCodexModels lists the chat models the ChatGPT account behind provider
// can pick, as served to the Codex CLI. Only entries with visibility "list"
// are returned (hidden ones are internal aliases), ordered by priority.
func FetchCodexModels(ctx context.Context, provider *store.LLMProviderData, tokenSource providers.TokenSource) ([]CodexModel, error) {
	if tokenSource == nil {
		return nil, fmt.Errorf("codex models: token source is required")
	}
	token, err := tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("codex models: get auth token: %w", err)
	}

	apiBase := strings.TrimRight(strings.TrimSpace(provider.APIBase), "/")
	if apiBase == "" {
		apiBase = DefaultProviderAPIBase
	}
	endpoint := apiBase + "/codex/models?client_version=" + url.QueryEscape(codexModelsClientVersion)
	accountID := strings.TrimSpace(parseOAuthSettings(provider.Settings).AccountID)

	// Retry transient failures (429/5xx honor Retry-After) so a single blip does
	// not swap the live catalog for the stale fallback list.
	all, err := providers.RetryDo(ctx, providers.RetryConfig{
		Attempts: 2,
		MinDelay: 400 * time.Millisecond,
		MaxDelay: 2 * time.Second,
		Jitter:   0.1,
	}, func() ([]CodexModel, error) {
		return requestCodexModels(ctx, endpoint, token, accountID)
	})
	if err != nil {
		return nil, err
	}

	models := make([]CodexModel, 0, len(all))
	for _, m := range all {
		m.Slug = strings.TrimSpace(m.Slug)
		if m.Slug == "" || m.Visibility != "list" {
			continue
		}
		models = append(models, m)
	}
	sort.SliceStable(models, func(i, j int) bool { return models[i].Priority < models[j].Priority })
	return models, nil
}

// requestCodexModels performs one catalog request and returns every entry.
func requestCodexModels(ctx context.Context, endpoint, token, accountID string) ([]CodexModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("codex models: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", codexModelsUserAgent)
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}

	resp, err := codexModelsHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex models: request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, codexModelsMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("codex models: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &providers.HTTPError{
			Status:     resp.StatusCode,
			Body:       truncateForError(string(body)),
			RetryAfter: providers.ParseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}

	var payload struct {
		Models []CodexModel `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("codex models: decode response: %w", err)
	}
	return payload.Models, nil
}

func truncateForError(s string) string {
	const limit = 512
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}
