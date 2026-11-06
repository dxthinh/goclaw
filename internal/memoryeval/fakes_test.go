package memoryeval

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type refreshCall struct {
	day    time.Time
	gauges bool
}

// fakeKPIStore giữ dữ liệu trong bộ nhớ và fail-closed khi thiếu tenant như store thật.
type fakeKPIStore struct {
	store.MemoryKPIStore
	mu         sync.Mutex
	results    []store.MemoryEvalResult
	countErr   error
	insertErr  error
	refreshErr error
	missing    []time.Time
	asked      []time.Time
	refreshes  []refreshCall
	prunes     []store.MemoryKPIPruneCutoff
}

func (f *fakeKPIStore) InsertEvalResult(ctx context.Context, r store.MemoryEvalResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if store.TenantIDFromContext(ctx) == uuid.Nil {
		return store.ErrMemoryKPITenantRequired
	}
	if f.insertErr != nil {
		return f.insertErr
	}
	f.results = append(f.results, r)
	return nil
}

func (f *fakeKPIStore) CountEvalsSince(ctx context.Context, _ time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if store.TenantIDFromContext(ctx) == uuid.Nil {
		return 0, store.ErrMemoryKPITenantRequired
	}
	return len(f.results), f.countErr
}

func (f *fakeKPIStore) RefreshDailyRollup(_ context.Context, day time.Time, gauges bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes = append(f.refreshes, refreshCall{day: day, gauges: gauges})
	return f.refreshErr
}

func (f *fakeKPIStore) MissingRollupDays(_ context.Context, days []time.Time) ([]time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = days
	return f.missing, nil
}

func (f *fakeKPIStore) PruneRawMetrics(_ context.Context, before store.MemoryKPIPruneCutoff) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prunes = append(f.prunes, before)
	return 0, nil
}

type fakeConfigs struct {
	store.SystemConfigStore
	values map[string]string
	getErr error
}

func (f *fakeConfigs) Get(_ context.Context, key string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	if v, ok := f.values[key]; ok {
		return v, nil
	}
	return "", store.ErrSystemConfigNotFound
}

func (f *fakeConfigs) List(_ context.Context) (map[string]string, error) {
	return map[string]string{"background.provider": "fake", "background.model": "fake-model"}, nil
}

// fakeProvider ghi lại request và trả phản hồi theo chatFn.
type fakeProvider struct {
	mu     sync.Mutex
	calls  int
	reqs   []providers.ChatRequest
	dead   []bool
	chatFn func() (*providers.ChatResponse, error)
}

func (p *fakeProvider) Chat(ctx context.Context, req providers.ChatRequest) (*providers.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.reqs = append(p.reqs, req)
	_, hasDeadline := ctx.Deadline()
	p.dead = append(p.dead, hasDeadline)
	return p.chatFn()
}

func (p *fakeProvider) ChatStream(ctx context.Context, req providers.ChatRequest, _ func(providers.StreamChunk)) (*providers.ChatResponse, error) {
	return p.Chat(ctx, req)
}

func (p *fakeProvider) DefaultModel() string { return "fake-model" }
func (p *fakeProvider) Name() string         { return "fake" }

func (p *fakeProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func respond(content string) func() (*providers.ChatResponse, error) {
	return func() (*providers.ChatResponse, error) {
		return &providers.ChatResponse{Content: content, Usage: &providers.Usage{PromptTokens: 11, CompletionTokens: 7}}, nil
	}
}

// evalFixture dựng Evaluator thật với provider giả; UsageCaps nil giống Lite.
type evalFixture struct {
	evaluator *Evaluator
	store     *fakeKPIStore
	configs   *fakeConfigs
	provider  *fakeProvider
	tenantID  uuid.UUID
}

func newEvalFixture(content string) *evalFixture {
	tenantID := uuid.New()
	f := &evalFixture{
		store:    &fakeKPIStore{},
		configs:  &fakeConfigs{values: map[string]string{}},
		provider: &fakeProvider{chatFn: respond(content)},
		tenantID: tenantID,
	}
	registry := providers.NewRegistry(store.TenantIDFromContext)
	registry.RegisterForTenant(tenantID, f.provider)
	f.evaluator = NewEvaluator(EvaluatorDeps{Store: f.store, SystemConfigs: f.configs, Registry: registry})
	return f
}

func (f *evalFixture) candidate() Candidate {
	return Candidate{
		TenantID:    f.tenantID,
		AgentID:     uuid.New(),
		TraceID:     uuid.New(),
		Channel:     store.MemoryEvalChannelAutoInject,
		Question:    "câu hỏi mẫu về lịch họp",
		Items:       []string{"mục một", "mục hai", "mục ba"},
		HasEpisodic: true,
		Answer:      "câu trả lời mẫu",
	}
}
