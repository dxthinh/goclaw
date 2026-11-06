package memory

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const injectTestMessage = "kubernetes deployment rollback plan"

type fakeEpisodicStore struct {
	store.EpisodicStore
	results []store.EpisodicSearchResult
	err     error
	calls   int
	query   string
	opts    store.EpisodicSearchOptions
}

func (f *fakeEpisodicStore) Search(_ context.Context, query, _, _ string, opts store.EpisodicSearchOptions) ([]store.EpisodicSearchResult, error) {
	f.calls++
	f.query, f.opts = query, opts
	return f.results, f.err
}

type fakeMetricsStore struct {
	store.EvolutionMetricsStore
	recorded chan store.EvolutionMetric
}

func newFakeMetricsStore() *fakeMetricsStore {
	return &fakeMetricsStore{recorded: make(chan store.EvolutionMetric, 1)}
}

func (f *fakeMetricsStore) RecordMetric(_ context.Context, m store.EvolutionMetric) error {
	f.recorded <- m
	return nil
}

// waitValue chờ goroutine ghi metric rồi giải mã trường value.
func (f *fakeMetricsStore) waitValue(t *testing.T) (store.EvolutionMetric, map[string]any) {
	t.Helper()
	select {
	case m := <-f.recorded:
		var value map[string]any
		if err := json.Unmarshal(m.Value, &value); err != nil {
			t.Fatalf("metric value is not JSON: %v", err)
		}
		return m, value
	case <-time.After(5 * time.Second):
		t.Fatal("metric was not recorded")
		return store.EvolutionMetric{}, nil
	}
}

type fakeConfigStore struct {
	store.SystemConfigStore
	values map[string]string
	err    error
}

func (f *fakeConfigStore) Get(_ context.Context, key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if v, ok := f.values[key]; ok {
		return v, nil
	}
	return "", store.ErrSystemConfigNotFound
}

func injectTestParams() InjectParams {
	return InjectParams{
		AgentID:     uuid.NewString(),
		TenantID:    uuid.NewString(),
		UserID:      "user-1",
		SessionKey:  "sess-1",
		UserMessage: injectTestMessage,
		CountTokens: func(text string) (int, error) { return len(text), nil },
	}
}

func hit(score float64, abstract string) store.EpisodicSearchResult {
	return store.EpisodicSearchResult{EpisodicID: uuid.NewString(), Score: score, L0Abstract: abstract}
}
