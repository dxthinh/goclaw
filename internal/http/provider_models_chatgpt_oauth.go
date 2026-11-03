package http

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/oauth"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// fetchChatGPTOAuthModels lists the chat models the ChatGPT account can use,
// read live from the Codex model catalog so new releases appear without a code
// change. Any failure (no token yet, upstream error, empty catalog) falls back
// to the static chatGPTOAuthModels list so the model picker never goes blank.
//
// When p owns a Codex pool, the router may send a request to any member, so
// only models every reachable account lists are offered.
func (h *ProvidersHandler) fetchChatGPTOAuthModels(ctx context.Context, p *store.LLMProviderData) []ModelInfo {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(loadProviderRequestTimeoutSec(ctx, h.sysConfigStore))*time.Second)
	defer cancel()

	catalog, err := h.fetchCodexCatalog(ctx, p)
	if err != nil || len(catalog) == 0 {
		slog.Warn("providers.models.chatgpt_oauth: using static catalog", "provider", p.Name, "error", err, "count", len(catalog))
		return chatGPTOAuthModels()
	}

	catalog = intersectCodexCatalogs(catalog, h.fetchPoolMemberCatalogs(ctx, p))
	if len(catalog) == 0 {
		slog.Warn("providers.models.chatgpt_oauth: pool accounts share no model, using static catalog", "provider", p.Name)
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

func (h *ProvidersHandler) fetchCodexCatalog(ctx context.Context, p *store.LLMProviderData) ([]oauth.CodexModel, error) {
	ts := oauth.NewDBTokenSource(h.store, h.secretStore, p.Name).WithTenantID(p.TenantID)
	return oauth.FetchCodexModels(ctx, p, ts)
}

// fetchPoolMemberCatalogs fetches the catalog of every Codex pool member of p
// concurrently. A member whose catalog cannot be read is skipped (logged): its
// entitlements are unknown, and dropping the whole list would blank the picker.
func (h *ProvidersHandler) fetchPoolMemberCatalogs(ctx context.Context, p *store.LLMProviderData) [][]oauth.CodexModel {
	settings := store.ParseChatGPTOAuthProviderSettings(p.Settings)
	if settings == nil || settings.CodexPool == nil {
		return nil
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		catalogs [][]oauth.CodexModel
	)
	for _, name := range settings.CodexPool.ExtraProviderNames {
		if name == "" || name == p.Name {
			continue
		}
		wg.Go(func() {
			member, err := lookupProviderByNameWithMasterFallback(ctx, h.store, p.TenantID, name)
			if err != nil || member.ProviderType != store.ProviderChatGPTOAuth {
				slog.Warn("providers.models.chatgpt_oauth: skip pool member", "provider", p.Name, "member", name, "error", err)
				return
			}
			catalog, err := h.fetchCodexCatalog(ctx, member)
			if err != nil || len(catalog) == 0 {
				slog.Warn("providers.models.chatgpt_oauth: skip pool member catalog", "provider", p.Name, "member", name, "error", err)
				return
			}
			mu.Lock()
			catalogs = append(catalogs, catalog)
			mu.Unlock()
		})
	}
	wg.Wait()
	return catalogs
}

// intersectCodexCatalogs keeps the owner's entries (and order) whose slug is
// listed by every member catalog.
func intersectCodexCatalogs(owner []oauth.CodexModel, members [][]oauth.CodexModel) []oauth.CodexModel {
	if len(members) == 0 {
		return owner
	}
	result := make([]oauth.CodexModel, 0, len(owner))
	for _, m := range owner {
		inAll := true
		for _, member := range members {
			if !slices.ContainsFunc(member, func(x oauth.CodexModel) bool { return x.Slug == m.Slug }) {
				inAll = false
				break
			}
		}
		if inAll {
			result = append(result, m)
		}
	}
	return result
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
