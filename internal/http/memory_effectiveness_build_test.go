package http

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

var (
	testThresholds = effectivenessThresholds{MinSamples: 30, KeepRelevance: 0.7, KeepUsed: 0.3, OffRelevance: 0.4}
	testSwitches   = effectivenessSwitches{AutoInjectEnabled: true, EvalEnabled: true}
	testWindow     = effectivenessWindow{From: "2026-11-01", To: "2026-11-07", Days: 7}
	kpiAgentA      = uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	kpiAgentB      = uuid.MustParse("00000000-0000-0000-0000-00000000000b")
)

func kpiRow(day int, agent uuid.UUID, metric, dim string, value float64) store.MemoryKPIDaily {
	return store.MemoryKPIDaily{
		BucketDate: time.Date(2026, 11, day, 0, 0, 0, 0, time.UTC),
		AgentID:    agent, Metric: metric, Dimension: dim, Value: value,
	}
}

func build(rows ...store.MemoryKPIDaily) effectivenessResponse {
	return buildEffectiveness(rows, testThresholds, testSwitches, testWindow)
}

func findLayer(t *testing.T, resp effectivenessResponse, name string) effectivenessLayer {
	t.Helper()
	for _, l := range resp.Layers {
		if l.Layer == name {
			return l
		}
	}
	t.Fatalf("layer %q missing", name)
	return effectivenessLayer{}
}

// Mọi nhánh verdict, kể cả đúng biên ngưỡng. Mẫu số chọn để phép chia ra đúng giá trị ngưỡng.
func TestBuildEffectiveness_VerdictBranches(t *testing.T) {
	cases := []struct {
		name                      string
		samples, items, rel, used float64
		want                      string
	}{
		{"thiếu mẫu sát biên", 29, 10, 10, 29, verdictInsufficientData},
		{"đủ mẫu đúng biên, liên quan thấp", 30, 100, 39, 30, verdictConsiderOff},
		{"liên quan đúng ngưỡng tắt thì không tắt", 30, 10, 4, 30, verdictTune},
		{"liên quan đúng ngưỡng giữ, được dùng đúng ngưỡng", 30, 10, 7, 9, verdictKeep},
		{"liên quan cao nhưng ít được dùng", 100, 10, 10, 29, verdictTune},
		{"được dùng cao nhưng liên quan dưới ngưỡng giữ", 30, 100, 69, 30, verdictTune},
		{"đủ mẫu nhưng không có mục nào được chấm", 30, 0, 0, 30, verdictInsufficientData},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := build(
				kpiRow(3, kpiAgentA, "eval_samples", "episodic", tc.samples),
				kpiRow(3, kpiAgentA, "eval_items", "episodic", tc.items),
				kpiRow(3, kpiAgentA, "eval_relevant_items", "episodic", tc.rel),
				kpiRow(3, kpiAgentA, "eval_used", "episodic", tc.used),
			)
			if got := findLayer(t, resp, "episodic").Verdict; got != tc.want {
				t.Fatalf("verdict = %s, want %s", got, tc.want)
			}
		})
	}
}

// Tính tay: ngày 2 có 10 mẫu, ngày 5 có 20 mẫu; items 40 và 60 (tổng 100), relevant 30 và 40
// (tổng 70), used 3 và 6 (tổng 9). Relevance = 70/100, used = 9/30.
func TestBuildEffectiveness_LayerRatiosSumAcrossDays(t *testing.T) {
	resp := build(
		kpiRow(2, kpiAgentA, "eval_samples", "document", 10), kpiRow(5, kpiAgentB, "eval_samples", "document", 20),
		kpiRow(2, kpiAgentA, "eval_items", "document", 40), kpiRow(5, kpiAgentB, "eval_items", "document", 60),
		kpiRow(2, kpiAgentA, "eval_relevant_items", "document", 30), kpiRow(5, kpiAgentB, "eval_relevant_items", "document", 40),
		kpiRow(2, kpiAgentA, "eval_used", "document", 3), kpiRow(5, kpiAgentB, "eval_used", "document", 6),
	)
	l := findLayer(t, resp, "document")
	if l.Samples != 30 || l.Relevance == nil || *l.Relevance != 0.7 || l.Used == nil || *l.Used != 0.3 {
		t.Fatalf("layer = %+v", l)
	}
	if l.Verdict != verdictKeep {
		t.Fatalf("verdict = %s", l.Verdict)
	}
}

func TestBuildEffectiveness_EmptyDataKeepsShapeAndNulls(t *testing.T) {
	resp := build()
	if len(resp.Funnel) != 3 || len(resp.Layers) != 5 {
		t.Fatalf("funnel=%d layers=%d", len(resp.Funnel), len(resp.Layers))
	}
	want := []string{"auto_inject", "memory_search", "episodic", "document", "knowledge_graph"}
	for i, l := range resp.Layers {
		if l.Layer != want[i] || l.Relevance != nil || l.Used != nil || l.Verdict != verdictInsufficientData {
			t.Fatalf("layer %d = %+v", i, l)
		}
	}
	if resp.Prompt != (effectivenessPrompt{}) {
		t.Fatalf("prompt = %+v", resp.Prompt)
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, frag := range []string{`"cache":[]`, `"background":[]`, `"relevance":null`, `"used":null`} {
		if !strings.Contains(s, frag) {
			t.Errorf("JSON thiếu %s: %s", frag, s)
		}
	}
}

// Tính tay, cộng hai ngày. auto_inject: disabled 2, trivial 3, no_match 4, below_threshold 5,
// no_abstract 6, injected 7, error 1 -> tổng 28, eligible 28-3-2 = 23, matched 6+7 = 13.
func TestBuildEffectiveness_Funnel(t *testing.T) {
	ai := func(day int, outcome string, v float64) store.MemoryKPIDaily {
		return kpiRow(day, kpiAgentA, "retrieval_events", "auto_inject:"+outcome, v)
	}
	resp := build(
		ai(1, "disabled", 2), ai(1, "trivial", 3), ai(1, "no_match", 4), ai(2, "below_threshold", 5),
		ai(2, "no_abstract", 6), ai(3, "injected", 4), ai(4, "injected", 3), ai(4, "error", 1),
		kpiRow(1, kpiAgentA, "retrieval_events", "memory_search:matched", 5),
		kpiRow(1, kpiAgentA, "retrieval_events", "memory_search:no_match", 3),
		kpiRow(2, kpiAgentB, "retrieval_events", "memory_search:error", 2),
		kpiRow(2, kpiAgentB, "retrieval_events", "memory_search:unknown", 1),
		kpiRow(1, kpiAgentA, "retrieval_events", "knowledge_graph_search:matched", 4),
		kpiRow(1, kpiAgentA, "retrieval_events", "knowledge_graph_search:no_match", 6),
	)
	want := []effectivenessFunnel{
		{Channel: "auto_inject", Eligible: 23, Searched: 23, Matched: 13, Injected: 7, Disabled: 2, Errors: 1},
		{Channel: "memory_search", Eligible: 11, Searched: 11, Matched: 5, Errors: 2},
		{Channel: "knowledge_graph_search", Eligible: 10, Searched: 10},
	}
	for i := range want {
		if resp.Funnel[i] != want[i] {
			t.Errorf("funnel[%d] = %+v, want %+v", i, resp.Funnel[i], want[i])
		}
	}
}

// Gauge không cộng qua ngày: agent A có 10 rồi 12 (lấy 12), agent B chỉ có 5 ở ngày cũ hơn
// (vẫn lấy 5). Tổng 17, không phải 27 hay 22.
func TestBuildEffectiveness_GaugeNotSummedAcrossDays(t *testing.T) {
	resp := build(
		kpiRow(1, kpiAgentA, "episodic_total", "", 10), kpiRow(6, kpiAgentA, "episodic_total", "", 12),
		kpiRow(2, kpiAgentB, "episodic_total", "", 5),
		kpiRow(1, kpiAgentA, "kg_entities", "", 8), kpiRow(6, kpiAgentA, "kg_entities", "", 9),
	)
	if got := resp.Pipeline.EpisodicTotal; got != 17 {
		t.Fatalf("episodic_total = %d, want 17", got)
	}
	if got := resp.Pipeline.KGEntities; got != 9 {
		t.Fatalf("kg_entities = %d, want 9", got)
	}
}

// 4 run (1 + 3): system 400 -> 100, memory 10 -> 2.5. Không có run thì mọi avg bằng 0.
func TestBuildEffectiveness_PromptAverages(t *testing.T) {
	resp := build(
		kpiRow(1, kpiAgentA, "prompt_runs", "", 1), kpiRow(2, kpiAgentA, "prompt_runs", "", 3),
		kpiRow(1, kpiAgentA, "prompt_system_tokens", "", 100), kpiRow(2, kpiAgentA, "prompt_system_tokens", "", 300),
		kpiRow(2, kpiAgentA, "prompt_memory_tokens", "", 10),
	)
	if resp.Prompt.Runs != 4 || resp.Prompt.Avg.System != 100 || resp.Prompt.Avg.Memory != 2.5 || resp.Prompt.Avg.History != 0 {
		t.Fatalf("prompt = %+v", resp.Prompt)
	}
	zero := build(kpiRow(1, kpiAgentA, "prompt_system_tokens", "", 100))
	if zero.Prompt.Avg.System != 0 {
		t.Fatalf("avg khi runs=0 = %v", zero.Prompt.Avg.System)
	}
}

func TestBuildEffectiveness_CacheRates(t *testing.T) {
	c := func(metric, dim string, v float64) store.MemoryKPIDaily { return kpiRow(3, kpiAgentA, metric, dim, v) }
	resp := build(
		c("llm_calls", "openai/gpt-x", 10), c("llm_cache_reporting_calls", "openai/gpt-x", 4),
		c("llm_cache_read_tokens", "openai/gpt-x", 300), c("llm_cache_denominator_tokens", "openai/gpt-x", 1200),
		// Provider không báo cache: nhiều call nhất nên đứng đầu, rate phải là nil chứ không phải 0.
		c("llm_calls", "anthropic/claude-x", 20), c("llm_cache_denominator_tokens", "anthropic/claude-x", 500),
		// Có báo nhưng mẫu số bằng 0.
		c("llm_calls", "openrouter/meta/llama", 5), c("llm_cache_reporting_calls", "openrouter/meta/llama", 5),
	)
	if len(resp.Cache) != 3 {
		t.Fatalf("cache = %+v", resp.Cache)
	}
	a, o, r := resp.Cache[0], resp.Cache[1], resp.Cache[2]
	if a.Provider != "anthropic" || a.Calls != 20 || a.Reporting || a.CacheRate != nil {
		t.Errorf("anthropic = %+v", a)
	}
	if o.Provider != "openai" || o.Model != "gpt-x" || !o.Reporting || o.CacheRate == nil || *o.CacheRate != 0.25 {
		t.Errorf("openai = %+v", o)
	}
	if r.Provider != "openrouter" || r.Model != "meta/llama" || !r.Reporting || r.CacheRate != nil {
		t.Errorf("openrouter = %+v", r)
	}
}

func TestBuildEffectiveness_BackgroundSortedByInputTokens(t *testing.T) {
	bg := func(metric, purpose string, v float64) store.MemoryKPIDaily {
		return kpiRow(3, kpiAgentA, metric, purpose, v)
	}
	resp := build(
		bg("bg_calls", "dreaming", 2), bg("bg_input_tokens", "dreaming", 100), bg("bg_output_tokens", "dreaming", 10),
		bg("bg_calls", "episodic-summary", 5), bg("bg_input_tokens", "episodic-summary", 900), bg("bg_output_tokens", "episodic-summary", 90),
	)
	got := resp.Pipeline.Background
	if len(got) != 2 || got[0].Purpose != "episodic-summary" || got[0].Calls != 5 || got[0].InputTokens != 900 || got[0].OutputTokens != 90 || got[1].Purpose != "dreaming" {
		t.Fatalf("background = %+v", got)
	}
}
