package caps

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	secretPrompt = "TOP-SECRET-PROMPT-TEXT"
	secretReply  = "TOP-SECRET-REPLY-TEXT"
	secretUserID = "user-secret-42"
)

// insertRecord là những gì store giả quan sát được ở mỗi lần InsertEvent.
type insertRecord struct {
	event       store.UsageEvent
	tenant      uuid.UUID
	ctxErr      error
	hasDeadline bool
}

// recordingEventStore là UsageEventStore giả: đẩy mỗi lần ghi vào kênh, có thể
// trả lỗi hoặc panic để kiểm tra việc ghi không ảnh hưởng lời gọi LLM.
type recordingEventStore struct {
	inserted chan insertRecord
	err      error
	panics   bool
}

func newRecordingEventStore() *recordingEventStore {
	return &recordingEventStore{inserted: make(chan insertRecord, 8)}
}

func (s *recordingEventStore) InsertEvent(ctx context.Context, event *store.UsageEvent) error {
	_, hasDeadline := ctx.Deadline()
	s.inserted <- insertRecord{
		event: *event, tenant: store.TenantIDFromContext(ctx),
		ctxErr: ctx.Err(), hasDeadline: hasDeadline,
	}
	if s.panics {
		panic("event store panic")
	}
	return s.err
}

func (s *recordingEventStore) InsertEvents(ctx context.Context, events []store.UsageEvent) error {
	for i := range events {
		if err := s.InsertEvent(ctx, &events[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *recordingEventStore) RefreshEventRollupHour(context.Context, time.Time) error { return nil }
func (s *recordingEventStore) GetLatestEventRollupBucket(context.Context) (*time.Time, error) {
	return nil, nil
}
func (s *recordingEventStore) GetEventTimeSeries(context.Context, store.UsageEventQuery) ([]store.UsageEventTimeSeries, error) {
	return nil, nil
}
func (s *recordingEventStore) GetEventBreakdown(context.Context, store.UsageEventQuery) ([]store.UsageEventBreakdown, error) {
	return nil, nil
}
func (s *recordingEventStore) GetEventSummary(context.Context, store.UsageEventQuery) (*store.UsageEventSummary, error) {
	return nil, nil
}

func (s *recordingEventStore) waitOne(t *testing.T) insertRecord {
	t.Helper()
	select {
	case rec := <-s.inserted:
		return rec
	case <-time.After(2 * time.Second):
		t.Fatal("không có usage event nào được ghi trong 2s")
		return insertRecord{}
	}
}

// expectNone khẳng định không có event nào được ghi thêm. Việc ghi chạy bất đồng
// bộ nên chờ một khoảng ngắn trước khi kết luận.
func (s *recordingEventStore) expectNone(t *testing.T) {
	t.Helper()
	select {
	case rec := <-s.inserted:
		t.Fatalf("không mong đợi usage event, nhận được %s/%s", rec.event.ResourceName, rec.event.Source)
	case <-time.After(150 * time.Millisecond):
	}
}

// scriptedProvider trả kết quả đặt trước; onChat chạy ngay trong lời gọi.
type scriptedProvider struct {
	name, model string
	resp        *providers.ChatResponse
	err         error
	onChat      func()
	calls       int
}

func (p *scriptedProvider) Chat(context.Context, providers.ChatRequest) (*providers.ChatResponse, error) {
	p.calls++
	if p.onChat != nil {
		p.onChat()
	}
	return p.resp, p.err
}

func (p *scriptedProvider) ChatStream(ctx context.Context, req providers.ChatRequest, _ func(providers.StreamChunk)) (*providers.ChatResponse, error) {
	return p.Chat(ctx, req)
}
func (p *scriptedProvider) DefaultModel() string { return p.model }
func (p *scriptedProvider) Name() string         { return p.name }

func okResponse() *providers.ChatResponse {
	return &providers.ChatResponse{
		Content: secretReply,
		Usage: &providers.Usage{
			PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120,
			CacheReadTokens: 60, CacheCreationTokens: 10, ThinkingTokens: 7,
			PromptTokensIncludeCachedSegments: true,
		},
	}
}

func promptRequest(model string) providers.ChatRequest {
	return providers.ChatRequest{
		Model:    model,
		Messages: []providers.Message{{Role: "user", Content: secretPrompt}},
	}
}

// serviceVariants phủ cả nhánh có store usage cap và nhánh s.store == nil.
func serviceVariants() map[string]*Service {
	return map[string]*Service{
		"with_cap_store":    NewService(&fakeUsageCapStore{}, &fakeProviderStore{}),
		"without_cap_store": {},
	}
}
