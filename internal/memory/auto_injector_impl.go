package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/safego"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Kết quả của một lượt auto-inject, ghi vào trường outcome của metric.
const (
	outcomeDisabled       = "disabled"
	outcomeTrivial        = "trivial"
	outcomeNoMatch        = "no_match"
	outcomeBelowThreshold = "below_threshold"
	outcomeNoAbstract     = "no_abstract"
	outcomeOverBudget     = "over_budget"
	outcomeInjected       = "injected"
	outcomeError          = "error"
)

// pgAutoInjector triển khai AutoInjector trên tìm kiếm của EpisodicStore.
type pgAutoInjector struct {
	episodicStore store.EpisodicStore
	metricsStore  store.EvolutionMetricsStore // nil = metrics disabled
	configStore   store.SystemConfigStore     // nil = dùng giá trị mặc định
}

// NewAutoInjector tạo AutoInjector. cs là nguồn công tắc và ngưỡng theo tenant; nil thì
// dùng mặc định.
func NewAutoInjector(es store.EpisodicStore, ms store.EvolutionMetricsStore, cs store.SystemConfigStore) AutoInjector {
	return &pgAutoInjector{episodicStore: es, metricsStore: ms, configStore: cs}
}

// MetricsStore trả về store metric của injector. Khác với store của Loop (chỉ được
// nối khi agent bật cờ self_evolution_metrics), store này luôn có, để metric KPI
// không phụ thuộc cờ theo agent.
func (a *pgAutoInjector) MetricsStore() store.EvolutionMetricsStore { return a.metricsStore }

// Inject tìm L0 abstract liên quan rồi định dạng khối memory cho lượt hiện tại.
// Mọi nhánh đều ghi metric, kể cả khi không chèn gì.
func (a *pgAutoInjector) Inject(ctx context.Context, params InjectParams) (*InjectResult, error) {
	result, err := a.evaluate(ctx, params)
	a.recordRetrievalMetric(params, result)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// evaluate luôn trả InjectResult có Outcome, kể cả khi có lỗi, để metric ghi đủ mọi nhánh.
func (a *pgAutoInjector) evaluate(ctx context.Context, params InjectParams) (*InjectResult, error) {
	if a.episodicStore == nil || !a.autoInjectEnabled(ctx) {
		return &InjectResult{Outcome: outcomeDisabled}, nil
	}
	if isTrivialMessage(params.UserMessage) {
		return &InjectResult{Outcome: outcomeTrivial}, nil
	}

	maxEntries := params.MaxEntries
	if maxEntries <= 0 {
		maxEntries = 5
	}

	// Embedding dùng truy vấn đã làm giàu để nắm ý đồ hội thoại; từ khóa chỉ dùng
	// tin nhắn người dùng vì nhãn Context:/Query: và ngữ cảnh cũ sẽ làm FTS trượt.
	// Không đặt MinScore: phải thấy điểm cao nhất kể cả khi dưới ngưỡng để hiệu chỉnh.
	results, err := a.episodicStore.Search(ctx, buildRecallQuery(params.UserMessage, params.RecentContext), params.AgentID, params.UserID,
		store.EpisodicSearchOptions{
			MaxResults:   maxEntries * 2,
			VectorWeight: 1.0,
			TextWeight:   1.0,
			TextQuery:    tailClipRunes(strings.TrimSpace(params.UserMessage), maxRecallTextRunes),
			MatchAnyTerm: true,
		})
	if err != nil {
		return &InjectResult{Outcome: outcomeError}, fmt.Errorf("auto-inject search: %w", err)
	}
	return evaluateMatchesWithBudget(results, a.autoInjectMinScore(ctx), maxEntries, params.MaxTokens, params.CountTokens), nil
}

// evaluateMatches áp ngưỡng lên kết quả đã sắp giảm dần theo điểm và dựng khối memory.
func evaluateMatches(results []store.EpisodicSearchResult, threshold float64, maxEntries int) *InjectResult {
	return evaluateMatchesWithBudget(results, threshold, maxEntries, 0, nil)
}

func evaluateMatchesWithBudget(results []store.EpisodicSearchResult, threshold float64, maxEntries, maxTokens int, count func(string) (int, error)) *InjectResult {
	if maxTokens <= 0 {
		maxTokens = DefaultMemoryConfig().AutoInjectMaxTokens
	}
	if maxEntries <= 0 {
		maxEntries = 5
	}
	budgetLimited := false
	result := &InjectResult{MatchCount: len(results)}
	abstracts := make([]string, 0, maxEntries)
	for _, r := range results {
		if r.Score > result.TopScore {
			result.TopScore = r.Score
		}
		if r.Score < threshold {
			continue
		}
		result.AboveThreshold++
		if abstract := strings.TrimSpace(r.L0Abstract); abstract != "" && len(abstracts) < maxEntries {
			candidate := append(abstracts, abstract)
			if sectionTokens("\n\n"+formatMemorySection(candidate), count) > maxTokens {
				budgetLimited = true
				continue
			}
			abstracts = candidate
		}
	}

	switch {
	case len(results) == 0:
		result.Outcome = outcomeNoMatch
	case result.AboveThreshold == 0:
		result.Outcome = outcomeBelowThreshold
	case len(abstracts) == 0 && budgetLimited:
		result.Outcome = outcomeOverBudget
	case len(abstracts) == 0:
		result.Outcome = outcomeNoAbstract
	default:
		result.Outcome = outcomeInjected
		result.Section = formatMemorySection(abstracts)
		result.Injected = len(abstracts)
	}
	return result
}

// recordRetrievalMetric ghi metric retrieval/auto_inject ở goroutine nền. Đếm token cũng
// nằm trong goroutine để không làm chậm luồng chính.
func (a *pgAutoInjector) recordRetrievalMetric(params InjectParams, result *InjectResult) {
	if a.metricsStore == nil || params.TenantID == "" {
		return
	}
	tenantID, err := uuid.Parse(params.TenantID)
	if err != nil {
		return
	}
	agentID, err := uuid.Parse(params.AgentID)
	if err != nil {
		return
	}
	go func() {
		defer safego.Recover(nil, "metric", "auto_inject")
		bgCtx, cancel := context.WithTimeout(store.WithTenantID(context.Background(), tenantID), 5*time.Second)
		defer cancel()
		value, err := json.Marshal(map[string]any{
			"outcome":         result.Outcome,
			"result_count":    result.MatchCount,
			"above_threshold": result.AboveThreshold,
			"injected":        result.Injected,
			"top_score":       result.TopScore,
			"memory_tokens":   sectionTokens(result.Section, params.CountTokens),
			"has_results":     result.MatchCount > 0,
		})
		if err != nil {
			slog.Debug("evolution.metric.auto_inject_marshal_failed", "error", err)
			return
		}
		if err := a.metricsStore.RecordMetric(bgCtx, store.EvolutionMetric{
			ID:         uuid.New(),
			TenantID:   tenantID,
			AgentID:    agentID,
			SessionKey: params.SessionKey,
			MetricType: store.MetricRetrieval,
			MetricKey:  "auto_inject",
			Value:      value,
		}); err != nil {
			slog.Debug("evolution.metric.auto_inject_failed", "error", err)
		}
	}()
}

// sectionTokens đếm token của khối memory; không có bộ đếm hoặc đếm lỗi thì ước lượng
// theo số rune, cùng cách ước lượng của bộ đếm dự phòng trong pipeline.
func sectionTokens(section string, count func(string) (int, error)) int {
	if section == "" {
		return 0
	}
	if count != nil {
		if n, err := count(section); err == nil {
			return n
		}
	}
	return utf8.RuneCountInString(section) / 3
}
