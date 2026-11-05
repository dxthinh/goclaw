package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tracing"
)

const internalCallSecret = "INTERNAL-CALL-SECRET-TEXT"

// internalCallProvider trả kết quả đặt trước cho lời gọi nội bộ.
type internalCallProvider struct {
	name, model string
	resp        *providers.ChatResponse
	err         error
	calls       int
}

func (p *internalCallProvider) Chat(context.Context, providers.ChatRequest) (*providers.ChatResponse, error) {
	p.calls++
	return p.resp, p.err
}

func (p *internalCallProvider) ChatStream(ctx context.Context, req providers.ChatRequest, _ func(providers.StreamChunk)) (*providers.ChatResponse, error) {
	return p.Chat(ctx, req)
}
func (p *internalCallProvider) DefaultModel() string { return p.model }
func (p *internalCallProvider) Name() string         { return p.name }

func internalCallResponse() *providers.ChatResponse {
	return &providers.ChatResponse{
		Content: internalCallSecret,
		Usage: &providers.Usage{
			PromptTokens: 300, CompletionTokens: 40, TotalTokens: 340,
			CacheReadTokens: 120, CacheCreationTokens: 30, ThinkingTokens: 5,
		},
	}
}

func internalCallRequest(model string) providers.ChatRequest {
	return providers.ChatRequest{
		Model:    model,
		Messages: []providers.Message{{Role: "user", Content: internalCallSecret}},
	}
}

// failingUsageEventStore vẫn ghi nhận lời gọi nhưng trả lỗi.
type failingUsageEventStore struct {
	*fakeUsageEventStore
}

func (s failingUsageEventStore) InsertEvent(ctx context.Context, event *store.UsageEvent) error {
	_ = s.fakeUsageEventStore.InsertEvent(ctx, event)
	return errors.New("db down")
}

func expectNoUsageEvent(t *testing.T, storeSpy *fakeUsageEventStore) {
	t.Helper()
	select {
	case event := <-storeSpy.events:
		t.Fatalf("không mong đợi usage event, nhận %s/%s", event.ResourceName, event.Source)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestCallInternalLLMWithUsageRecordsBackgroundEvent(t *testing.T) {
	storeSpy := newFakeUsageEventStore()
	provider := &internalCallProvider{name: "anthropic", model: "claude-default", resp: internalCallResponse()}
	loop := &Loop{provider: provider, model: "claude-default", usageEvents: storeSpy, agentUUID: uuid.New(), tenantID: uuid.New()}
	traceID := uuid.New()
	ctx := tracing.WithTraceID(t.Context(), traceID)
	ctx = store.WithRunContext(ctx, &store.RunContext{RunID: "run-1", SessionKey: "session-1", Channel: "telegram", UserID: "user-secret-7"})

	resp, err := loop.callInternalLLMWithUsage(ctx, internalCallRequest("claude-x"), "mid-loop-compaction")
	if err != nil || resp == nil || resp.Content != internalCallSecret {
		t.Fatalf("callInternalLLMWithUsage = %+v, %v", resp, err)
	}

	event := waitUsageEvent(t, storeSpy)
	if event.EventType != store.UsageEventTypeLLMBackground || event.ResourceType != store.UsageResourceTypeLLMPurpose {
		t.Fatalf("event type/resource = %q/%q", event.EventType, event.ResourceType)
	}
	if event.ResourceName != "mid-loop-compaction" || event.Source != store.UsageSourceAgentInternal {
		t.Fatalf("purpose/source = %q/%q", event.ResourceName, event.Source)
	}
	if event.TenantID != loop.tenantID {
		t.Fatalf("tenant = %s, want tenant của agent %s khi ctx không có tenant", event.TenantID, loop.tenantID)
	}
	if event.AgentID == nil || *event.AgentID != loop.agentUUID {
		t.Fatalf("agent = %v, want %s", event.AgentID, loop.agentUUID)
	}
	if event.TraceID == nil || *event.TraceID != traceID || event.SessionKey != "session-1" || event.RunID != "run-1" {
		t.Fatalf("trace/session/run = %v/%q/%q", event.TraceID, event.SessionKey, event.RunID)
	}
	if event.Provider != "anthropic" || event.Model != "claude-x" {
		t.Fatalf("provider/model = %q/%q", event.Provider, event.Model)
	}
	if event.InputTokens != 300 || event.OutputTokens != 40 || event.TotalTokens != 340 ||
		event.CacheReadTokens != 120 || event.CacheCreateTokens != 30 || event.ThinkingTokens != 5 {
		t.Fatalf("token fields sai: %+v", event)
	}
	if event.Status != "completed" || event.ErrorCount != 0 {
		t.Fatalf("status/error = %q/%d", event.Status, event.ErrorCount)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	for _, leaked := range []string{internalCallSecret, "user-secret-7"} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("event làm lộ %q: %s", leaked, raw)
		}
	}
	expectNoUsageEvent(t, storeSpy)
}

func TestCallInternalLLMWithUsageRecordsProviderError(t *testing.T) {
	storeSpy := newFakeUsageEventStore()
	boom := errors.New("provider unavailable")
	provider := &internalCallProvider{name: "anthropic", model: "claude-default", err: boom}
	tenantID := uuid.New()
	loop := &Loop{provider: provider, model: "claude-default", usageEvents: storeSpy, agentUUID: uuid.New()}
	ctx := store.WithTenantID(t.Context(), tenantID)

	resp, err := loop.callInternalLLMWithUsage(ctx, internalCallRequest(""), "memory-flush")
	if !errors.Is(err, boom) || resp != nil {
		t.Fatalf("callInternalLLMWithUsage = %+v, %v; want lỗi gốc", resp, err)
	}
	event := waitUsageEvent(t, storeSpy)
	if event.Status != "error" || event.ErrorCount != 1 || event.ResourceName != "memory-flush" {
		t.Fatalf("status/error/purpose = %q/%d/%q", event.Status, event.ErrorCount, event.ResourceName)
	}
	if event.TenantID != tenantID || event.Model != "claude-default" {
		t.Fatalf("tenant/model = %s/%q", event.TenantID, event.Model)
	}
	expectNoUsageEvent(t, storeSpy)
}

func TestCallInternalLLMWithUsageFallbackRecordsSingleEvent(t *testing.T) {
	storeSpy := newFakeUsageEventStore()
	primary := &internalCallProvider{name: "primary", model: "primary-model", err: &providers.HTTPError{Status: 429, Body: "rate limited"}}
	backup := &internalCallProvider{name: "backup", model: "backup-model", resp: internalCallResponse()}
	fallback := providers.NewModelFallbackProvider(
		providers.FallbackCandidate{ProviderName: "primary", Model: "primary-model", Provider: primary},
		[]providers.FallbackCandidate{{ProviderName: "backup", Model: "backup-model", Provider: backup}},
		2, false)
	loop := &Loop{provider: fallback, model: "primary-model", usageEvents: storeSpy, agentUUID: uuid.New(), tenantID: uuid.New()}

	resp, err := loop.callInternalLLMWithUsage(t.Context(), internalCallRequest(""), "memory-flush")
	if err != nil || resp == nil {
		t.Fatalf("callInternalLLMWithUsage = %+v, %v", resp, err)
	}
	if primary.calls != 1 || backup.calls != 1 {
		t.Fatalf("calls primary=%d backup=%d, want 1/1", primary.calls, backup.calls)
	}
	event := waitUsageEvent(t, storeSpy)
	if event.Provider != "backup" || event.Model != "backup-model" || event.Status != "completed" {
		t.Fatalf("event = %s/%s status=%s, want candidate thắng cuộc", event.Provider, event.Model, event.Status)
	}
	if event.ResourceName != "memory-flush" {
		t.Fatalf("purpose = %q, không được chứa provider/model của candidate", event.ResourceName)
	}
	var meta map[string]any
	if err := json.Unmarshal(event.Metadata, &meta); err != nil || meta["attempts"] != float64(2) {
		t.Fatalf("metadata = %s, want attempts=2", event.Metadata)
	}
	expectNoUsageEvent(t, storeSpy)
}

func TestCallInternalLLMWithUsageToleratesEventStoreProblems(t *testing.T) {
	t.Run("no event store", func(t *testing.T) {
		provider := &internalCallProvider{name: "p", model: "m", resp: internalCallResponse()}
		loop := &Loop{provider: provider, model: "m"}
		resp, err := loop.callInternalLLMWithUsage(t.Context(), internalCallRequest("m"), "memory-flush")
		if err != nil || resp == nil {
			t.Fatalf("callInternalLLMWithUsage = %+v, %v", resp, err)
		}
	})
	t.Run("insert fails", func(t *testing.T) {
		storeSpy := newFakeUsageEventStore()
		provider := &internalCallProvider{name: "p", model: "m", resp: internalCallResponse()}
		loop := &Loop{provider: provider, model: "m", usageEvents: failingUsageEventStore{storeSpy}, agentUUID: uuid.New(), tenantID: uuid.New()}
		resp, err := loop.callInternalLLMWithUsage(t.Context(), internalCallRequest("m"), "memory-flush")
		if err != nil || resp == nil || resp.Content != internalCallSecret {
			t.Fatalf("callInternalLLMWithUsage = %+v, %v; lỗi ghi event không được ảnh hưởng kết quả", resp, err)
		}
		waitUsageEvent(t, storeSpy)
	})
}
