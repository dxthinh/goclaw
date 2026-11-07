// Package memory extends the v3 memory system with auto-injection and tiered retrieval.
//
// V3 design: Phase 3 — L0/L1/L2 context tiering + smart auto-inject.
package memory

import "context"

// AutoInjector kiểm tra độ liên quan và tạo khối memory L0 cho lượt hiện tại.
// Called once per turn in ContextStage.
type AutoInjector interface {
	// Inject checks user message against memory index.
	// Trả khối đã định dạng để gửi kèm lượt hiện tại, hoặc "" nếu không có gì liên quan.
	// Budget: MaxTokens includes the formatted section and its context separator.
	Inject(ctx context.Context, params InjectParams) (*InjectResult, error)
}

// InjectParams configures a single auto-inject call.
type InjectParams struct {
	AgentID     string
	UserID      string
	TenantID    string
	SessionKey  string // gắn vào metric để truy ngược theo phiên
	UserMessage string

	// RecentContext carries a short snippet of recent conversation (typically
	// the last 1-2 user turns concatenated) used to enrich the search query.
	// Context-aware recall: without this, vector search on "what's my favorite?"
	// misses memories about the topic under discussion. With it, the query
	// embedding captures conversational intent and returns materially better
	// matches for follow-up questions.
	//
	// Empty = legacy behaviour (search on UserMessage only).
	// Target length: ≤ ~400 chars. Longer context dilutes the embedding.
	RecentContext string

	MaxEntries int // default 5
	MaxTokens  int // default 200, including formatting and context separator

	// CountTokens đếm token của khối memory cho metric. Truyền từ bộ đếm sẵn có của
	// agent để không nạp thêm một bộ mã hóa; nil thì ước lượng theo số rune.
	CountTokens func(text string) (int, error)
}

// InjectResult contains the injection output + observability data.
type InjectResult struct {
	Section        string  // khối memory đã định dạng (rỗng = không có gì liên quan)
	Outcome        string  // disabled, trivial, no_match, below_threshold, no_abstract, over_budget, injected, error
	MatchCount     int     // total matches found
	AboveThreshold int     // số kết quả đạt ngưỡng
	Injected       int     // entries injected (after budget trim)
	TopScore       float64 // điểm cao nhất, kể cả khi dưới ngưỡng
}

// L0Summary là một mục auto-inject.
type L0Summary struct {
	Topic   string // short topic label
	Summary string // ~1 sentence abstract
	ID      string // for memory_expand(id) deep retrieval
}

// MemoryConfig provides legacy defaults; tenant switches and thresholds are read from system_configs.
type MemoryConfig struct {
	AutoInjectEnabled    bool    `json:"auto_inject_enabled"`    // default true
	AutoInjectThreshold  float64 `json:"auto_inject_threshold"`  // default 0.8
	AutoInjectMaxTokens  int     `json:"auto_inject_max_tokens"` // default 200
	EpisodicTTLDays      int     `json:"episodic_ttl_days"`      // default 90
	ConsolidationEnabled bool    `json:"consolidation_enabled"`  // default true
}

// DefaultMemoryConfig returns sensible defaults.
func DefaultMemoryConfig() MemoryConfig {
	return MemoryConfig{
		AutoInjectEnabled:    true,
		AutoInjectThreshold:  defaultAutoInjectMinScore,
		AutoInjectMaxTokens:  200,
		EpisodicTTLDays:      90,
		ConsolidationEnabled: true,
	}
}
