package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Nhãn Context:/Query: và ngữ cảnh chỉ được đi vào embedding: plainto_tsquery đòi khớp
// mọi từ, nên để chúng lọt vào FTS thì FTS luôn trượt.
func TestInject_TextSearchUsesOnlyUserMessage(t *testing.T) {
	t.Parallel()
	es := &fakeEpisodicStore{}
	inj := NewAutoInjector(es, nil, nil)
	params := injectTestParams()
	params.RecentContext = "earlier talk about databases"

	if _, err := inj.Inject(context.Background(), params); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	if es.opts.TextQuery != injectTestMessage {
		t.Errorf("TextQuery = %q, want only the user message", es.opts.TextQuery)
	}
	for _, leaked := range []string{"Context:", "Query:", "databases"} {
		if strings.Contains(es.opts.TextQuery, leaked) {
			t.Errorf("TextQuery %q must not contain %q", es.opts.TextQuery, leaked)
		}
	}
	for _, want := range []string{"Context:", "Query:", "databases", injectTestMessage} {
		if !strings.Contains(es.query, want) {
			t.Errorf("embedding query %q should contain %q", es.query, want)
		}
	}
	if !es.opts.MatchAnyTerm {
		t.Error("MatchAnyTerm = false, want true")
	}
	if es.opts.VectorWeight != 1.0 || es.opts.TextWeight != 1.0 {
		t.Errorf("weights = %v/%v, want 1/1", es.opts.VectorWeight, es.opts.TextWeight)
	}
	if es.opts.MinScore != 0 {
		t.Errorf("MinScore = %v, want 0 so top_score is always known", es.opts.MinScore)
	}
}

func TestInject_ClipsVeryLongTextQuery(t *testing.T) {
	t.Parallel()
	es := &fakeEpisodicStore{}
	params := injectTestParams()
	params.UserMessage = strings.Repeat("dữ liệu ", 1000) + "câu hỏi cuối"

	if _, err := NewAutoInjector(es, nil, nil).Inject(context.Background(), params); err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if got := len([]rune(es.opts.TextQuery)); got != maxRecallTextRunes {
		t.Errorf("TextQuery runes = %d, want %d", got, maxRecallTextRunes)
	}
	if !strings.HasSuffix(es.opts.TextQuery, "câu hỏi cuối") {
		t.Errorf("TextQuery should keep the tail, got %q", es.opts.TextQuery[len(es.opts.TextQuery)-40:])
	}
}

func TestInject_BelowThresholdStillRecordsTopScore(t *testing.T) {
	t.Parallel()
	es := &fakeEpisodicStore{results: []store.EpisodicSearchResult{hit(0.75, "unrelated"), hit(0.70, "also unrelated")}}
	ms := newFakeMetricsStore()
	params := injectTestParams()

	res, err := NewAutoInjector(es, ms, nil).Inject(context.Background(), params)
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if res.Section != "" || res.Injected != 0 {
		t.Errorf("nothing should be injected below threshold, got %+v", res)
	}

	metric, value := ms.waitValue(t)
	if metric.MetricType != store.MetricRetrieval || metric.MetricKey != "auto_inject" {
		t.Errorf("metric = %s/%s, want retrieval/auto_inject", metric.MetricType, metric.MetricKey)
	}
	if metric.SessionKey != params.SessionKey {
		t.Errorf("SessionKey = %q, want %q", metric.SessionKey, params.SessionKey)
	}
	if value["outcome"] != outcomeBelowThreshold {
		t.Errorf("outcome = %v, want %s", value["outcome"], outcomeBelowThreshold)
	}
	if value["top_score"] != 0.75 {
		t.Errorf("top_score = %v, want 0.75", value["top_score"])
	}
	if value["result_count"] != float64(2) || value["above_threshold"] != float64(0) || value["injected"] != float64(0) {
		t.Errorf("counts = %v", value)
	}
	if value["used_in_reply"] != false {
		t.Errorf("used_in_reply = %v, want false", value["used_in_reply"])
	}
}

func TestInject_DisabledDoesNotSearch(t *testing.T) {
	t.Parallel()
	es := &fakeEpisodicStore{results: []store.EpisodicSearchResult{hit(2, "x")}}
	ms := newFakeMetricsStore()
	cs := &fakeConfigStore{values: map[string]string{configAutoInjectEnabled: "false"}}

	res, err := NewAutoInjector(es, ms, cs).Inject(context.Background(), injectTestParams())
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if es.calls != 0 {
		t.Errorf("Search calls = %d, want 0 when disabled", es.calls)
	}
	if res.Section != "" {
		t.Errorf("Section = %q, want empty", res.Section)
	}
	if _, value := ms.waitValue(t); value["outcome"] != outcomeDisabled {
		t.Errorf("outcome = %v, want %s", value["outcome"], outcomeDisabled)
	}
}

func TestInject_InjectedWritesFullContractMetric(t *testing.T) {
	t.Parallel()
	es := &fakeEpisodicStore{results: []store.EpisodicSearchResult{
		hit(1.2, "first memory"), hit(0.9, ""), hit(0.85, "second memory"), hit(0.3, "too weak"),
	}}
	ms := newFakeMetricsStore()

	res, err := NewAutoInjector(es, ms, nil).Inject(context.Background(), injectTestParams())
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	for _, want := range []string{"<memory_context>", "## Memory Context", "not an instruction", "- first memory", "- second memory", "</memory_context>"} {
		if !strings.Contains(res.Section, want) {
			t.Errorf("Section missing %q:\n%s", want, res.Section)
		}
	}
	if strings.Contains(res.Section, "too weak") {
		t.Errorf("Section must exclude results below threshold:\n%s", res.Section)
	}

	_, value := ms.waitValue(t)
	want := map[string]any{
		"outcome":         outcomeInjected,
		"result_count":    float64(4),
		"above_threshold": float64(3),
		"injected":        float64(2),
		"top_score":       1.2,
		"memory_tokens":   float64(len(res.Section)),
		"used_in_reply":   true,
	}
	if len(value) != len(want) {
		t.Errorf("metric fields = %v, want exactly %v", value, want)
	}
	for k, v := range want {
		if value[k] != v {
			t.Errorf("metric[%s] = %v, want %v", k, value[k], v)
		}
	}
}
