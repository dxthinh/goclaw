package caps

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestNilReceiverAndMissingEventStoreAreSafe(t *testing.T) {
	provider := &scriptedProvider{name: "p", model: "m", resp: okResponse()}

	var nilSvc *Service
	nilSvc.SetUsageEventStore(newRecordingEventStore())
	if resp, err := nilSvc.Chat(context.Background(), provider, promptRequest("m"), ChatOptions{Purpose: "x"}); err != nil || resp == nil {
		t.Fatalf("nil receiver Chat = %+v, %v", resp, err)
	}

	for name, svc := range map[string]*Service{
		"cap store, no events":    NewService(&fakeUsageCapStore{}, &fakeProviderStore{}),
		"no cap store, no events": {},
	} {
		if resp, err := svc.Chat(context.Background(), provider, promptRequest("m"), ChatOptions{Purpose: "x"}); err != nil || resp == nil {
			t.Fatalf("%s: Chat = %+v, %v", name, resp, err)
		}
	}
	if provider.calls != 3 {
		t.Fatalf("provider calls = %d, want 3", provider.calls)
	}
	if _, err := (&Service{}).Chat(context.Background(), nil, promptRequest("m"), ChatOptions{}); err == nil {
		t.Fatal("provider nil phải trả lỗi")
	}
}

func TestChatFallbackRecordsSingleEvent(t *testing.T) {
	rateLimited := &providers.HTTPError{Status: 429, Body: "rate limited"}
	newFallback := func(primaryErr, backupErr error) (*providers.ModelFallbackProvider, *scriptedProvider, *scriptedProvider) {
		primary := &scriptedProvider{name: "primary", model: "primary-model", err: primaryErr}
		backup := &scriptedProvider{name: "backup", model: "backup-model", err: backupErr}
		if backupErr == nil {
			backup.resp = okResponse()
		}
		fb := providers.NewModelFallbackProvider(
			providers.FallbackCandidate{ProviderName: "primary", Model: "primary-model", Provider: primary},
			[]providers.FallbackCandidate{{ProviderName: "backup", Model: "backup-model", Provider: backup}},
			2, false)
		return fb, primary, backup
	}

	for name, svc := range serviceVariants() {
		t.Run(name+"/second candidate succeeds", func(t *testing.T) {
			events := newRecordingEventStore()
			svc.SetUsageEventStore(events)
			fb, primary, backup := newFallback(rateLimited, nil)

			resp, err := svc.Chat(context.Background(), fb, promptRequest(""), ChatOptions{Purpose: "kg-extract:primary:primary-model"})
			if err != nil || resp == nil {
				t.Fatalf("Chat = %+v, %v", resp, err)
			}
			if primary.calls != 1 || backup.calls != 1 {
				t.Fatalf("calls primary=%d backup=%d, want 1/1", primary.calls, backup.calls)
			}
			ev := events.waitOne(t).event
			if ev.Provider != "backup" || ev.Model != "backup-model" || ev.Status != "completed" || ev.ErrorCount != 0 {
				t.Fatalf("event = %s/%s status=%s err=%d, want candidate thắng cuộc", ev.Provider, ev.Model, ev.Status, ev.ErrorCount)
			}
			if ev.InputTokens != 100 || ev.OutputTokens != 20 {
				t.Fatalf("token = %d/%d, chỉ tính usage của lời gọi thành công", ev.InputTokens, ev.OutputTokens)
			}
			var meta map[string]any
			if err := json.Unmarshal(ev.Metadata, &meta); err != nil || meta["attempts"] != float64(2) {
				t.Fatalf("metadata = %s, want attempts=2", ev.Metadata)
			}
			events.expectNone(t)
		})
		t.Run(name+"/every candidate fails", func(t *testing.T) {
			events := newRecordingEventStore()
			svc.SetUsageEventStore(events)
			fb, _, _ := newFallback(rateLimited, rateLimited)

			if _, err := svc.Chat(context.Background(), fb, promptRequest(""), ChatOptions{Purpose: "kg-extract"}); err == nil {
				t.Fatal("mong đợi lỗi khi mọi candidate đều thất bại")
			}
			ev := events.waitOne(t).event
			if ev.Status != "error" || ev.ErrorCount != 1 || ev.Provider != "backup" {
				t.Fatalf("event = %s status=%s err=%d, want một event lỗi của candidate cuối", ev.Provider, ev.Status, ev.ErrorCount)
			}
			events.expectNone(t)
		})
	}
}

func TestChatEventStoreFailureDoesNotAffectResult(t *testing.T) {
	cases := map[string]*recordingEventStore{
		"insert error": {inserted: make(chan insertRecord, 8), err: errors.New("db down")},
		"insert panic": {inserted: make(chan insertRecord, 8), panics: true},
	}
	for name, events := range cases {
		for variant, svc := range serviceVariants() {
			t.Run(name+"/"+variant, func(t *testing.T) {
				svc.SetUsageEventStore(events)
				provider := &scriptedProvider{name: "p", model: "m", resp: okResponse()}

				resp, err := svc.Chat(context.Background(), provider, promptRequest("m"), ChatOptions{Purpose: "x"})
				if err != nil || resp == nil || resp.Content != secretReply {
					t.Fatalf("Chat = %+v, %v; lỗi ghi event không được ảnh hưởng kết quả", resp, err)
				}
				events.waitOne(t)
			})
		}
	}
}

func TestChatRecordsWithDetachedContextKeepingTenant(t *testing.T) {
	tenantID := uuid.New()
	events := newRecordingEventStore()
	svc := &Service{}
	svc.SetUsageEventStore(events)
	ctx, cancel := context.WithCancel(store.WithTenantID(context.Background(), tenantID))
	defer cancel()
	// Hủy context ngay trong lời gọi để chắc chắn lúc ghi context gốc đã bị hủy.
	provider := &scriptedProvider{name: "p", model: "m", resp: okResponse(), onChat: cancel}

	if _, err := svc.Chat(ctx, provider, promptRequest("m"), ChatOptions{Purpose: "x"}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	rec := events.waitOne(t)
	if rec.ctxErr != nil {
		t.Fatalf("context ghi event đã bị hủy: %v", rec.ctxErr)
	}
	if rec.tenant != tenantID {
		t.Fatalf("tenant trong context ghi = %s, want %s", rec.tenant, tenantID)
	}
	if !rec.hasDeadline {
		t.Fatal("context ghi event phải có timeout")
	}
}

func TestNormalizePurpose(t *testing.T) {
	cases := []struct{ in, want string }{
		{"episodic-summary", "episodic-summary"},
		{"episodic-summary:openai:gpt-4o", "episodic-summary"},
		{"mid-loop-compaction:anthropic:claude-x", "mid-loop-compaction"},
		{"channel-delivery-quick_ack", "channel-delivery"},
		{"channel-delivery-intermediate_progress:p:m", "channel-delivery"},
		{"channel-delivery", "channel-delivery"},
		{"  memory-flush  ", "memory-flush"},
		{"", "unspecified"},
		{":provider:model", "unspecified"},
		{"   ", "unspecified"},
		{strings.Repeat("a", 200), strings.Repeat("a", maxPurposeLength)},
	}
	for _, tc := range cases {
		if got := NormalizePurpose(tc.in); got != tc.want {
			t.Errorf("NormalizePurpose(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
