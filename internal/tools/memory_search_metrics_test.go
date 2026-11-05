package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type memorySearchFakeMetricsStore struct {
	store.EvolutionMetricsStore
	recorded chan store.EvolutionMetric
}

func (f *memorySearchFakeMetricsStore) RecordMetric(_ context.Context, m store.EvolutionMetric) error {
	f.recorded <- m
	return nil
}

type memorySearchErrStore struct {
	store.MemoryStore
	results []store.MemorySearchResult
	err     error
}

func (f *memorySearchErrStore) Search(context.Context, string, string, string, store.MemorySearchOptions) ([]store.MemorySearchResult, error) {
	return f.results, f.err
}

type memorySearchErrEpisodicStore struct {
	store.EpisodicStore
	results []store.EpisodicSearchResult
	err     error
}

func (f *memorySearchErrEpisodicStore) Search(context.Context, string, string, string, store.EpisodicSearchOptions) ([]store.EpisodicSearchResult, error) {
	return f.results, f.err
}

func (f *memorySearchErrEpisodicStore) RecordRecall(context.Context, string, float64) error {
	return nil
}

func TestMemorySearch_RecordsOutcomeAndHits(t *testing.T) {
	t.Parallel()
	dbDown := errors.New("db down")
	docs := []store.MemorySearchResult{{Path: "MEMORY.md", Score: 0.9}, {Path: "notes.md", Score: 0.5}}
	eps := []store.EpisodicSearchResult{{EpisodicID: uuid.NewString(), L0Abstract: "x", Score: 0.7}}

	tests := []struct {
		name         string
		args         map[string]any
		docs         []store.MemorySearchResult
		docErr       error
		eps          []store.EpisodicSearchResult
		epErr        error
		wantOutcome  string
		wantDocs     float64
		wantEpisodic float64
		wantUsed     bool
		wantTop      float64
	}{
		{name: "both tiers hit", args: map[string]any{"query": "plan"}, docs: docs, eps: eps, wantOutcome: "matched", wantDocs: 2, wantEpisodic: 1, wantUsed: true, wantTop: 0.7},
		{name: "documents only", args: map[string]any{"query": "plan"}, docs: docs, wantOutcome: "matched", wantDocs: 2, wantUsed: true},
		{name: "documents survive episodic failure", args: map[string]any{"query": "plan"}, docs: docs, epErr: dbDown, wantOutcome: "matched", wantDocs: 2, wantUsed: true},
		{name: "nothing found", args: map[string]any{"query": "plan"}, wantOutcome: "no_match"},
		{name: "episodic failure with no results", args: map[string]any{"query": "plan"}, epErr: dbDown, wantOutcome: "error"},
		{name: "document failure", args: map[string]any{"query": "plan"}, docErr: dbDown, wantOutcome: "error"},
		{name: "missing query", args: map[string]any{}, wantOutcome: "error"},
		{name: "invalid filter", args: map[string]any{"query": "plan", "createdAfter": "not-a-date"}, wantOutcome: "error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ms := &memorySearchFakeMetricsStore{recorded: make(chan store.EvolutionMetric, 1)}
			tool := NewMemorySearchTool()
			tool.SetMemoryStore(&memorySearchErrStore{results: tt.docs, err: tt.docErr})
			tool.SetEpisodicStore(&memorySearchErrEpisodicStore{results: tt.eps, err: tt.epErr})
			tool.SetEvolutionMetricsStore(ms)
			ctx := store.WithTenantID(context.Background(), uuid.New())
			ctx = store.WithAgentID(ctx, uuid.New())
			ctx = WithToolSessionKey(ctx, "sess-1")

			tool.Execute(ctx, tt.args)

			select {
			case metric := <-ms.recorded:
				var value map[string]any
				if err := json.Unmarshal(metric.Value, &value); err != nil {
					t.Fatalf("metric value: %v", err)
				}
				if metric.MetricKey != "memory_search" || metric.SessionKey != "sess-1" {
					t.Errorf("metric key/session = %s/%s", metric.MetricKey, metric.SessionKey)
				}
				want := map[string]any{
					"outcome":       tt.wantOutcome,
					"document_hits": tt.wantDocs,
					"episodic_hits": tt.wantEpisodic,
					"result_count":  tt.wantDocs + tt.wantEpisodic,
					"top_score":     tt.wantTop,
					"used_in_reply": tt.wantUsed,
				}
				for k, v := range want {
					if value[k] != v {
						t.Errorf("metric[%s] = %v, want %v", k, value[k], v)
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("metric was not recorded")
			}
		})
	}
}
