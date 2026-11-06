package tools

import (
	"encoding/json"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

func TestSubagentCacheMetadata(t *testing.T) {
	if got := subagentCacheMetadata(&providers.Usage{PromptTokens: 100}); got != nil {
		t.Fatalf("provider không báo cache phải cho nil, được %s", got)
	}

	var meta map[string]any
	raw := subagentCacheMetadata(&providers.Usage{PromptTokens: 1000, CacheReadTokens: 800, PromptTokensIncludeCachedSegments: true})
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("metadata không hợp lệ: %v", err)
	}
	if meta["prompt_tokens_include_cached_segments"] != true || meta["cache_read_tokens"] != float64(800) {
		t.Fatalf("metadata = %v, muốn có cờ gộp cache và số token đọc từ cache", meta)
	}

	// Chỉ có cờ, chưa đọc được gì từ cache: vẫn phải ghi để rollup biết provider có báo cache.
	if subagentCacheMetadata(&providers.Usage{PromptTokens: 1000, PromptTokensIncludeCachedSegments: true}) == nil {
		t.Fatal("cờ gộp cache phải được ghi kể cả khi chưa đọc được gì từ cache")
	}
}
