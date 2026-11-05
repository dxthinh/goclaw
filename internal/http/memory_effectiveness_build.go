package http

import (
	"math"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Nhãn kết luận của một tầng memory.
const (
	verdictKeep             = "keep"
	verdictTune             = "tune"
	verdictConsiderOff      = "consider_off"
	verdictInsufficientData = "insufficient_data"
)

// effectivenessLayerOrder là thứ tự cố định của năm tầng trong phản hồi.
var effectivenessLayerOrder = []string{"auto_inject", "memory_search", "episodic", "document", "knowledge_graph"}

// effectivenessChannelOrder là thứ tự cố định của ba kênh truy hồi trong phễu.
var effectivenessChannelOrder = []string{
	store.MemoryEvalChannelAutoInject,
	store.MemoryEvalChannelMemorySearch,
	store.MemoryEvalChannelKnowledgeGraph,
}

type effectivenessWindow struct {
	From string `json:"from"`
	To   string `json:"to"`
	Days int    `json:"days"`
}

type effectivenessThresholds struct {
	MinSamples    int     `json:"min_samples"`
	KeepRelevance float64 `json:"keep_relevance"`
	KeepUsed      float64 `json:"keep_used"`
	OffRelevance  float64 `json:"off_relevance"`
}

type effectivenessSwitches struct {
	AutoInjectEnabled bool `json:"auto_inject_enabled"`
	EvalEnabled       bool `json:"eval_enabled"`
}

type effectivenessFunnel struct {
	Channel  string `json:"channel"`
	Eligible int64  `json:"eligible"`
	Searched int64  `json:"searched"`
	Matched  int64  `json:"matched"`
	Injected int64  `json:"injected"`
	Disabled int64  `json:"disabled"`
	Errors   int64  `json:"errors"`
}

type effectivenessLayer struct {
	Layer     string   `json:"layer"`
	Samples   int64    `json:"samples"`
	Relevance *float64 `json:"relevance"`
	Used      *float64 `json:"used"`
	Verdict   string   `json:"verdict"`
}

type effectivenessPromptAvg struct {
	System     float64 `json:"system"`
	ToolSchema float64 `json:"tool_schema"`
	History    float64 `json:"history"`
	ToolResult float64 `json:"tool_result"`
	Memory     float64 `json:"memory"`
}

type effectivenessPrompt struct {
	Runs int64                  `json:"runs"`
	Avg  effectivenessPromptAvg `json:"avg"`
}

type effectivenessCache struct {
	Provider  string   `json:"provider"`
	Model     string   `json:"model"`
	Calls     int64    `json:"calls"`
	CacheRate *float64 `json:"cache_rate"`
	Reporting bool     `json:"reporting"`
}

type effectivenessBackground struct {
	Purpose      string `json:"purpose"`
	Calls        int64  `json:"calls"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

type effectivenessPipeline struct {
	EpisodicTotal     int64                     `json:"episodic_total"`
	EpisodicStale     int64                     `json:"episodic_stale"`
	EpisodicPromoted  int64                     `json:"episodic_promoted"`
	KGEntities        int64                     `json:"kg_entities"`
	KGRelations       int64                     `json:"kg_relations"`
	MemoryDocuments   int64                     `json:"memory_documents"`
	DreamingDocuments int64                     `json:"dreaming_documents"`
	Background        []effectivenessBackground `json:"background"`
}

type effectivenessResponse struct {
	Window     effectivenessWindow     `json:"window"`
	Funnel     []effectivenessFunnel   `json:"funnel"`
	Layers     []effectivenessLayer    `json:"layers"`
	Prompt     effectivenessPrompt     `json:"prompt"`
	Cache      []effectivenessCache    `json:"cache"`
	Pipeline   effectivenessPipeline   `json:"pipeline"`
	Thresholds effectivenessThresholds `json:"thresholds"`
	Switches   effectivenessSwitches   `json:"switches"`
}

// effectivenessAcc gom số liệu của một nhóm dimension.
type effectivenessAcc map[string]float64

// buildEffectiveness là hàm thuần: cộng dồn các metric cộng dồn trong cả khoảng, và với
// metric giá trị cuối ngày chỉ lấy ngày mới nhất có dữ liệu của từng agent rồi cộng các agent.
func buildEffectiveness(rows []store.MemoryKPIDaily, th effectivenessThresholds, sw effectivenessSwitches, window effectivenessWindow) effectivenessResponse {
	flat := effectivenessAcc{}             // metric -> tổng (dimension rỗng hoặc gộp)
	byDim := map[string]effectivenessAcc{} // metric -> dimension -> tổng
	gaugeRows := map[uuid.UUID][]store.MemoryKPIDaily{}
	latestGauge := map[uuid.UUID]string{}

	for _, r := range rows {
		if isGaugeMetric(r.Metric) {
			day := store.MemoryKPIDayKey(r.BucketDate)
			gaugeRows[r.AgentID] = append(gaugeRows[r.AgentID], r)
			if day > latestGauge[r.AgentID] {
				latestGauge[r.AgentID] = day
			}
			continue
		}
		flat[r.Metric] += r.Value
		if byDim[r.Metric] == nil {
			byDim[r.Metric] = effectivenessAcc{}
		}
		byDim[r.Metric][r.Dimension] += r.Value
	}
	for agent, list := range gaugeRows {
		for _, r := range list {
			if store.MemoryKPIDayKey(r.BucketDate) == latestGauge[agent] {
				flat[r.Metric] += r.Value
			}
		}
	}

	return effectivenessResponse{
		Window:     window,
		Funnel:     buildFunnel(byDim["retrieval_events"]),
		Layers:     buildLayers(byDim, th),
		Prompt:     buildPrompt(flat),
		Cache:      buildCache(byDim),
		Pipeline:   buildPipeline(flat, byDim),
		Thresholds: th,
		Switches:   sw,
	}
}

func isGaugeMetric(metric string) bool {
	switch metric {
	case "episodic_total", "episodic_stale", "episodic_promoted", "kg_entities", "kg_relations", "memory_documents", "dreaming_documents":
		return true
	}
	return false
}

func count(v float64) int64 { return int64(math.Round(v)) }

func buildFunnel(events effectivenessAcc) []effectivenessFunnel {
	totals := map[string]float64{}
	outcomes := map[string]float64{} // "channel:outcome" -> số
	for dim, v := range events {
		channel, _, ok := strings.Cut(dim, ":")
		if !ok {
			continue
		}
		totals[channel] += v
		outcomes[dim] += v
	}
	out := make([]effectivenessFunnel, 0, len(effectivenessChannelOrder))
	for _, ch := range effectivenessChannelOrder {
		total := totals[ch]
		oc := func(name string) float64 { return outcomes[ch+":"+name] }
		f := effectivenessFunnel{Channel: ch, Errors: count(oc("error"))}
		switch ch {
		case store.MemoryEvalChannelAutoInject:
			eligible := total - oc("trivial") - oc("disabled")
			f.Disabled = count(oc("disabled"))
			f.Eligible = count(eligible)
			f.Searched = f.Eligible
			f.Matched = count(oc("no_abstract") + oc("injected"))
			f.Injected = count(oc("injected"))
		case store.MemoryEvalChannelMemorySearch:
			f.Eligible = count(total)
			f.Searched = f.Eligible
			f.Matched = count(oc("matched"))
		default:
			f.Eligible = count(total)
			f.Searched = f.Eligible
		}
		out = append(out, f)
	}
	return out
}

func ratio(num, den float64) *float64 {
	if den <= 0 {
		return nil
	}
	v := num / den
	return &v
}

// layerVerdict áp thứ tự: thiếu mẫu, liên quan thấp, đạt cả hai ngưỡng giữ, còn lại là tune.
// Khi đủ mẫu mà không có mục nào được chấm (relevance nil) thì chưa thể kết luận.
func layerVerdict(samples int64, relevance, used *float64, th effectivenessThresholds) string {
	switch {
	case samples < int64(th.MinSamples) || relevance == nil || used == nil:
		return verdictInsufficientData
	case *relevance < th.OffRelevance:
		return verdictConsiderOff
	case *relevance >= th.KeepRelevance && *used >= th.KeepUsed:
		return verdictKeep
	default:
		return verdictTune
	}
}

func buildLayers(byDim map[string]effectivenessAcc, th effectivenessThresholds) []effectivenessLayer {
	out := make([]effectivenessLayer, 0, len(effectivenessLayerOrder))
	for _, layer := range effectivenessLayerOrder {
		samples := byDim["eval_samples"][layer]
		relevance := ratio(byDim["eval_relevant_items"][layer], byDim["eval_items"][layer])
		used := ratio(byDim["eval_used"][layer], samples)
		out = append(out, effectivenessLayer{
			Layer: layer, Samples: count(samples), Relevance: relevance, Used: used,
			Verdict: layerVerdict(count(samples), relevance, used, th),
		})
	}
	return out
}

func buildPrompt(flat effectivenessAcc) effectivenessPrompt {
	runs := flat["prompt_runs"]
	avg := func(metric string) float64 {
		if runs <= 0 {
			return 0
		}
		return flat[metric] / runs
	}
	return effectivenessPrompt{Runs: count(runs), Avg: effectivenessPromptAvg{
		System:     avg("prompt_system_tokens"),
		ToolSchema: avg("prompt_tool_schema_tokens"),
		History:    avg("prompt_history_tokens"),
		ToolResult: avg("prompt_tool_result_tokens"),
		Memory:     avg("prompt_memory_tokens"),
	}}
}

func buildCache(byDim map[string]effectivenessAcc) []effectivenessCache {
	dims := map[string]bool{}
	for _, m := range []string{"llm_calls", "llm_cache_reporting_calls", "llm_cache_read_tokens", "llm_cache_denominator_tokens"} {
		for d := range byDim[m] {
			dims[d] = true
		}
	}
	out := make([]effectivenessCache, 0, len(dims))
	for d := range dims {
		provider, model, _ := strings.Cut(d, "/")
		reporting := byDim["llm_cache_reporting_calls"][d] > 0
		var rate *float64
		if reporting {
			rate = ratio(byDim["llm_cache_read_tokens"][d], byDim["llm_cache_denominator_tokens"][d])
		}
		out = append(out, effectivenessCache{
			Provider: provider, Model: model, Calls: count(byDim["llm_calls"][d]), CacheRate: rate, Reporting: reporting,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Model < out[j].Model
	})
	return out
}

func buildPipeline(flat effectivenessAcc, byDim map[string]effectivenessAcc) effectivenessPipeline {
	purposes := map[string]bool{}
	for _, m := range []string{"bg_calls", "bg_input_tokens", "bg_output_tokens"} {
		for p := range byDim[m] {
			purposes[p] = true
		}
	}
	bg := make([]effectivenessBackground, 0, len(purposes))
	for p := range purposes {
		bg = append(bg, effectivenessBackground{
			Purpose: p, Calls: count(byDim["bg_calls"][p]),
			InputTokens: count(byDim["bg_input_tokens"][p]), OutputTokens: count(byDim["bg_output_tokens"][p]),
		})
	}
	sort.Slice(bg, func(i, j int) bool {
		if bg[i].InputTokens != bg[j].InputTokens {
			return bg[i].InputTokens > bg[j].InputTokens
		}
		return bg[i].Purpose < bg[j].Purpose
	})
	return effectivenessPipeline{
		EpisodicTotal: count(flat["episodic_total"]), EpisodicStale: count(flat["episodic_stale"]),
		EpisodicPromoted: count(flat["episodic_promoted"]), KGEntities: count(flat["kg_entities"]),
		KGRelations: count(flat["kg_relations"]), MemoryDocuments: count(flat["memory_documents"]),
		DreamingDocuments: count(flat["dreaming_documents"]), Background: bg,
	}
}
