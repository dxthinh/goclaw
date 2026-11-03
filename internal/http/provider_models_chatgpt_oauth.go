package http

import (
	"context"
	"log/slog"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/oauth"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// fetchChatGPTOAuthModels lists the chat models the ChatGPT account can use,
// read live from the Codex model catalog so new releases appear without a code
// change. Any failure (no token yet, upstream error, empty catalog) falls back
// to the static chatGPTOAuthModels list so the model picker never goes blank.
func (h *ProvidersHandler) fetchChatGPTOAuthModels(ctx context.Context, p *store.LLMProviderData) []ModelInfo {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(loadProviderRequestTimeoutSec(ctx, h.sysConfigStore))*time.Second)
	defer cancel()

	ts := oauth.NewDBTokenSource(h.store, h.secretStore, p.Name).WithTenantID(p.TenantID)
	catalog, err := oauth.FetchCodexModels(ctx, p, ts)
	if err != nil || len(catalog) == 0 {
		slog.Warn("providers.models.chatgpt_oauth: using static catalog", "provider", p.Name, "error", err, "count", len(catalog))
		return chatGPTOAuthModels()
	}

	models := make([]ModelInfo, 0, len(catalog))
	for _, m := range catalog {
		name := m.DisplayName
		if name == "" {
			name = m.Slug
		}
		models = append(models, ModelInfo{
			ID:        m.Slug,
			Name:      name,
			Reasoning: codexReasoningCapability(m),
		})
	}
	return models
}

// codexReasoningCapability prefers GoClaw's curated capability table (it encodes
// levels such as "none" that the catalog omits) and otherwise derives one from
// the catalog entry, keeping only effort levels the request pipeline understands.
func codexReasoningCapability(m oauth.CodexModel) *providers.ReasoningCapability {
	if known := providers.LookupReasoningCapability(m.Slug); known != nil {
		return known
	}
	levels := make([]string, 0, len(m.SupportedReasoningLevels))
	for _, opt := range m.SupportedReasoningLevels {
		if level := supportedReasoningLevel(opt.Effort); level != "" {
			levels = append(levels, level)
		}
	}
	if len(levels) == 0 {
		return nil
	}
	capability := &providers.ReasoningCapability{Levels: levels}
	if def := supportedReasoningLevel(m.DefaultReasoningLevel); def != "" && capability.Supports(def) {
		capability.DefaultEffort = def
	}
	return capability
}

// supportedReasoningLevel returns the canonical level when it is a concrete
// effort GoClaw can send upstream; mode values ("off", "auto") are not levels.
func supportedReasoningLevel(effort string) string {
	switch level := providers.NormalizeReasoningEffort(effort); level {
	case "", "off", "auto":
		return ""
	default:
		return level
	}
}

// nativeImageModelInfos exposes the image_generation model catalog to the UI.
func nativeImageModelInfos() []ModelInfo {
	catalog := providers.NativeImageModels()
	models := make([]ModelInfo, 0, len(catalog))
	for _, m := range catalog {
		models = append(models, ModelInfo{ID: m.ID, Name: m.Name})
	}
	return models
}
