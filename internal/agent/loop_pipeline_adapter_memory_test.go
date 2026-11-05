package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/memory"
	"github.com/nextlevelbuilder/goclaw/internal/pipeline"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type adapterFakeMetricsStore struct {
	store.EvolutionMetricsStore
	metrics []store.EvolutionMetric
}

func (f *adapterFakeMetricsStore) RecordMetric(_ context.Context, m store.EvolutionMetric) error {
	f.metrics = append(f.metrics, m)
	return nil
}

type adapterFakeEpisodicStore struct{ store.EpisodicStore }

type adapterFakeInjector struct{ params memory.InjectParams }

func (f *adapterFakeInjector) Inject(_ context.Context, params memory.InjectParams) (*memory.InjectResult, error) {
	f.params = params
	return &memory.InjectResult{Section: "MEM"}, nil
}

// Metric prompt/shape phải có ở mọi agent, kể cả agent không bật self_evolution_metrics
// (lúc đó evolutionMetricsStore của Loop là nil).
func TestRecordPromptShape_DoesNotDependOnAgentFlag(t *testing.T) {
	t.Parallel()
	ms := &adapterFakeMetricsStore{}
	agentID := uuid.New()
	l := &Loop{agentUUID: agentID, autoInjector: memory.NewAutoInjector(adapterFakeEpisodicStore{}, ms, nil)}

	record := l.makeRecordPromptShape(&RunRequest{SessionKey: "sess-1"})
	if record == nil {
		t.Fatal("callback is nil, want a metric store from the injector")
	}
	tenantID := uuid.New()
	record(store.WithTenantID(context.Background(), tenantID), pipeline.PromptShape{
		SystemTokens: 1, ToolSchemaTokens: 2, HistoryTokens: 3, ToolResultTokens: 4, MemoryTokens: 5,
		TotalEstimated: 15, Provider: "p", Model: "m",
	})

	if len(ms.metrics) != 1 {
		t.Fatalf("metrics = %d, want 1", len(ms.metrics))
	}
	m := ms.metrics[0]
	if m.MetricType != store.MetricPrompt || m.MetricKey != "shape" {
		t.Errorf("metric = %s/%s, want prompt/shape", m.MetricType, m.MetricKey)
	}
	if m.SessionKey != "sess-1" || m.AgentID != agentID || m.TenantID != tenantID {
		t.Errorf("metric identity = %+v", m)
	}
	var value map[string]any
	if err := json.Unmarshal(m.Value, &value); err != nil {
		t.Fatalf("value: %v", err)
	}
	want := map[string]any{
		"system_tokens": 1.0, "tool_schema_tokens": 2.0, "history_tokens": 3.0, "tool_result_tokens": 4.0,
		"memory_tokens": 5.0, "total_estimated": 15.0, "provider": "p", "model": "m",
	}
	if len(value) != len(want) {
		t.Errorf("value = %v, want exactly %v", value, want)
	}
	for k, v := range want {
		if value[k] != v {
			t.Errorf("value[%s] = %v, want %v", k, value[k], v)
		}
	}
}

func TestRecordPromptShape_NoStoreOrTenant(t *testing.T) {
	t.Parallel()
	if got := (&Loop{}).makeRecordPromptShape(&RunRequest{}); got != nil {
		t.Error("callback must be nil without a metrics store")
	}
	ms := &adapterFakeMetricsStore{}
	l := &Loop{evolutionMetricsStore: ms}
	l.makeRecordPromptShape(&RunRequest{})(context.Background(), pipeline.PromptShape{})
	if len(ms.metrics) != 0 {
		t.Errorf("metrics = %d, want 0 without a tenant in ctx", len(ms.metrics))
	}
}

func TestAutoInjectCallback_PassesSessionKey(t *testing.T) {
	t.Parallel()
	inj := &adapterFakeInjector{}
	l := &Loop{agentUUID: uuid.New(), autoInjector: inj}

	section, err := l.makeAutoInjectCallback(&RunRequest{SessionKey: "sess-1"})(context.Background(), "msg", "user", "recent")
	if err != nil || section != "MEM" {
		t.Fatalf("callback = %q, %v", section, err)
	}
	if inj.params.SessionKey != "sess-1" {
		t.Errorf("SessionKey = %q, want sess-1", inj.params.SessionKey)
	}
}
