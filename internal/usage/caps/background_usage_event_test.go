package caps

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tracing"
)

func TestChatRecordsBackgroundUsageEvent(t *testing.T) {
	tenantID, agentID, traceID := uuid.New(), uuid.New(), uuid.New()
	for name, svc := range serviceVariants() {
		t.Run(name, func(t *testing.T) {
			events := newRecordingEventStore()
			svc.SetUsageEventStore(events)
			ctx := store.WithTenantID(context.Background(), tenantID)
			ctx = store.WithAgentID(ctx, agentID)
			ctx = tracing.WithTraceID(ctx, traceID)
			ctx = store.WithRunContext(ctx, &store.RunContext{
				RunID: "run-1", SessionKey: "session-1", Channel: "telegram", UserID: secretUserID,
			})
			provider := &scriptedProvider{name: "openrouter", model: "default/model", resp: okResponse()}

			resp, err := svc.Chat(ctx, provider, promptRequest("vendor/model"), ChatOptions{
				Purpose: "episodic-summary:openrouter:vendor/model", MaxOutputTokens: 256,
			})
			if err != nil || resp == nil || resp.Content != secretReply {
				t.Fatalf("Chat = %+v, %v; want phản hồi gốc không đổi", resp, err)
			}

			rec := events.waitOne(t)
			ev := rec.event
			if ev.EventType != store.UsageEventTypeLLMBackground || ev.ResourceType != store.UsageResourceTypeLLMPurpose {
				t.Fatalf("event type/resource = %q/%q", ev.EventType, ev.ResourceType)
			}
			if ev.ResourceName != "episodic-summary" || ev.ResourceID != "episodic-summary" {
				t.Fatalf("purpose chưa chuẩn hóa: name=%q id=%q", ev.ResourceName, ev.ResourceID)
			}
			if ev.Source != store.UsageSourceUsageCaps {
				t.Fatalf("source = %q", ev.Source)
			}
			if ev.TenantID != tenantID || rec.tenant != tenantID {
				t.Fatalf("tenant event=%s ctx=%s, want %s", ev.TenantID, rec.tenant, tenantID)
			}
			if ev.AgentID == nil || *ev.AgentID != agentID {
				t.Fatalf("agent = %v, want %s", ev.AgentID, agentID)
			}
			if ev.TraceID == nil || *ev.TraceID != traceID {
				t.Fatalf("trace = %v, want %s", ev.TraceID, traceID)
			}
			if ev.RunID != "run-1" || ev.SessionKey != "session-1" || ev.Channel != "telegram" {
				t.Fatalf("run/session/channel = %q/%q/%q", ev.RunID, ev.SessionKey, ev.Channel)
			}
			if ev.Provider != "openrouter" || ev.Model != "vendor/model" {
				t.Fatalf("provider/model = %q/%q", ev.Provider, ev.Model)
			}
			if ev.InputTokens != 100 || ev.OutputTokens != 20 || ev.TotalTokens != 120 ||
				ev.CacheReadTokens != 60 || ev.CacheCreateTokens != 10 || ev.ThinkingTokens != 7 {
				t.Fatalf("token fields sai: %+v", ev)
			}
			if ev.Status != "completed" || ev.ErrorCount != 0 || ev.CallCount != 1 {
				t.Fatalf("status/error/call = %q/%d/%d", ev.Status, ev.ErrorCount, ev.CallCount)
			}
			if !ev.BucketHour.Equal(ev.EventTime.Truncate(time.Hour)) || ev.DurationMS < 0 {
				t.Fatalf("bucket/duration sai: %v %v %d", ev.BucketHour, ev.EventTime, ev.DurationMS)
			}
			var meta map[string]any
			if err := json.Unmarshal(ev.Metadata, &meta); err != nil || meta["prompt_tokens_include_cached_segments"] != true {
				t.Fatalf("metadata = %s (%v)", ev.Metadata, err)
			}

			raw, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal event: %v", err)
			}
			for _, leaked := range []string{secretPrompt, secretReply, secretUserID} {
				if strings.Contains(string(raw), leaked) {
					t.Fatalf("event làm lộ %q: %s", leaked, raw)
				}
			}
			events.expectNone(t)
		})
	}
}

func TestChatDefaultsTenantAndPurpose(t *testing.T) {
	events := newRecordingEventStore()
	svc := &Service{}
	svc.SetUsageEventStore(events)
	provider := &scriptedProvider{name: "p", model: "m-default", resp: okResponse()}

	if _, err := svc.Chat(context.Background(), provider, promptRequest(""), ChatOptions{}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	ev := events.waitOne(t).event
	if ev.TenantID != store.MasterTenantID || ev.ResourceName != "unspecified" || ev.AgentID != nil {
		t.Fatalf("tenant/purpose/agent = %s/%q/%v", ev.TenantID, ev.ResourceName, ev.AgentID)
	}
	if ev.Model != "m-default" {
		t.Fatalf("model = %q, want model mặc định của provider", ev.Model)
	}
}

func TestChatProviderErrorRecordsErrorEvent(t *testing.T) {
	boom := errors.New("provider unavailable")
	for name, svc := range serviceVariants() {
		t.Run(name, func(t *testing.T) {
			events := newRecordingEventStore()
			svc.SetUsageEventStore(events)
			provider := &scriptedProvider{name: "openrouter", model: "m", err: boom}

			resp, err := svc.Chat(context.Background(), provider, promptRequest("m"), ChatOptions{Purpose: "dreaming-synthesis"})
			if !errors.Is(err, boom) || resp != nil {
				t.Fatalf("Chat = %+v, %v; want lỗi gốc", resp, err)
			}
			ev := events.waitOne(t).event
			if ev.Status != "error" || ev.ErrorCount != 1 || ev.ResourceName != "dreaming-synthesis" {
				t.Fatalf("status/error/purpose = %q/%d/%q", ev.Status, ev.ErrorCount, ev.ResourceName)
			}
			if ev.InputTokens != 0 || ev.OutputTokens != 0 || ev.TotalTokens != 0 {
				t.Fatalf("lỗi không có usage nhưng token = %d/%d/%d", ev.InputTokens, ev.OutputTokens, ev.TotalTokens)
			}
		})
	}
}

func TestChatBlockedBeforeProviderRecordsNothing(t *testing.T) {
	policy := store.UsageCapPolicy{ID: uuid.New(), TenantID: uuid.New(), MaxTokens: int64Ptr(10), Enabled: true}
	capStore := &fakeUsageCapStore{
		policies:   []store.UsageCapPolicy{policy},
		reserveErr: &store.UsageCapExceededError{PolicyID: policy.ID, Reason: "token_cap_exceeded"},
	}
	providerStore := &fakeProviderStore{provider: &store.LLMProviderData{
		BaseModel: store.BaseModel{ID: uuid.New()}, Name: "openrouter",
		ProviderType: store.ProviderOpenRouter, APIKey: "sk-test",
	}}
	events := newRecordingEventStore()
	svc := NewService(capStore, providerStore)
	svc.SetUsageEventStore(events)
	provider := &scriptedProvider{name: "openrouter", model: "m", resp: okResponse()}

	_, err := svc.Chat(context.Background(), provider, promptRequest("m"), ChatOptions{
		TenantID: policy.TenantID, ProviderName: "openrouter", Purpose: "blocked",
	})
	if !errors.Is(err, ErrCapExceeded) || provider.calls != 0 {
		t.Fatalf("err=%v calls=%d, want ErrCapExceeded và chưa gọi provider", err, provider.calls)
	}
	events.expectNone(t)
}
