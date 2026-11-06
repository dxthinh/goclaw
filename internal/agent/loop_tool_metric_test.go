package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/memory"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// channelMetricsStore báo từng metric qua channel vì recordToolMetric ghi ở goroutine nền.
type channelMetricsStore struct {
	store.EvolutionMetricsStore
	recorded chan store.EvolutionMetric
}

func (s *channelMetricsStore) RecordMetric(_ context.Context, m store.EvolutionMetric) error {
	s.recorded <- m
	return nil
}

// knowledge_graph_search là một kênh của phễu KPI nên phải được ghi kèm outcome ở mọi agent, kể
// cả agent không bật self_evolution_metrics (lúc đó evolutionMetricsStore của Loop là nil).
func TestRecordToolMetric_KnowledgeGraphDoesNotDependOnAgentFlag(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		result *tools.Result
		want   string
	}{
		{"có kết quả", tools.NewResult("Found 1 entities"), "matched"},
		{"không có kết quả", &tools.Result{ForLLM: "No entities", NoMatch: true}, "no_match"},
		{"tool lỗi", tools.ErrorResult("entity search failed"), "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ms := &channelMetricsStore{recorded: make(chan store.EvolutionMetric, 1)}
			agentID := uuid.New()
			l := &Loop{agentUUID: agentID, autoInjector: memory.NewAutoInjector(adapterFakeEpisodicStore{}, ms, nil)}

			l.recordToolMetric(context.Background(), "sess-1", toolKnowledgeGraphSearch, tc.result, time.Millisecond)

			select {
			case m := <-ms.recorded:
				var value map[string]any
				if err := json.Unmarshal(m.Value, &value); err != nil {
					t.Fatalf("value không hợp lệ: %v", err)
				}
				if m.MetricType != store.MetricTool || m.MetricKey != toolKnowledgeGraphSearch || m.AgentID != agentID {
					t.Errorf("metric = %+v", m)
				}
				if value["outcome"] != tc.want {
					t.Errorf("outcome = %v, muốn %s", value["outcome"], tc.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("không có metric nào được ghi")
			}
		})
	}
}

// Tool khác vẫn phụ thuộc cờ self_evolution_metrics như trước.
func TestRecordToolMetric_OtherToolsStillNeedAgentFlag(t *testing.T) {
	t.Parallel()
	ms := &channelMetricsStore{recorded: make(chan store.EvolutionMetric, 1)}
	l := &Loop{agentUUID: uuid.New(), autoInjector: memory.NewAutoInjector(adapterFakeEpisodicStore{}, ms, nil)}

	l.recordToolMetric(context.Background(), "sess-1", "web_search", tools.NewResult("ok"), time.Millisecond)

	select {
	case m := <-ms.recorded:
		t.Fatalf("không được ghi metric khi agent chưa bật cờ: %+v", m)
	case <-time.After(100 * time.Millisecond):
	}
}
