package caps

import (
	"context"
	"log/slog"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/safego"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// backgroundUsageInsertTimeout giới hạn thời gian ghi một usage event nền.
const backgroundUsageInsertTimeout = 5 * time.Second

// CallTrace gom những gì xảy ra ở phía provider trong một lời gọi LLM: số lần
// gọi thật, candidate cuối cùng và mốc thời gian. Một lời gọi có fallback theo
// candidate chỉ cho ra đúng một event, nên mọi nhánh đều dùng chung struct này.
// Giá trị zero dùng được; không an toàn khi dùng đồng thời từ nhiều goroutine.
type CallTrace struct {
	started      time.Time
	attempts     int
	providerName string
	modelID      string
	lastResp     *providers.ChatResponse
}

// Begin báo sắp gọi provider thật với provider/model này. Gọi sau mọi bước chặn
// (guard, preflight) để lần bị chặn không bị tính là đã gọi.
func (t *CallTrace) Begin(providerName, modelID string) {
	if t.started.IsZero() {
		t.started = time.Now()
	}
	t.providerName, t.modelID = providerName, modelID
}

// End báo provider đã trả về (kể cả lỗi). Mỗi lần gọi Begin phải có một End.
func (t *CallTrace) End(resp *providers.ChatResponse) {
	t.attempts++
	t.lastResp = resp
}

// Record ghi một usage event nền nếu đã có ít nhất một lần gọi provider thật.
// Không chặn: việc ghi chạy ở goroutine riêng, context tách hủy nhưng giữ tenant,
// có timeout. Lỗi ghi chỉ được log, không ảnh hưởng kết quả lời gọi LLM.
// Nil receiver hoặc events == nil là no-op.
func (t *CallTrace) Record(ctx context.Context, events store.UsageEventStore, usage BackgroundUsage, resp *providers.ChatResponse, callErr error) {
	if t == nil || events == nil || t.attempts == 0 {
		return
	}
	if resp == nil {
		resp = t.lastResp
	}
	event := buildBackgroundEvent(ctx, t, usage, resp, callErr, time.Now())
	detached := context.WithoutCancel(ctx)
	go func() {
		defer safego.Recover(nil, "component", "llm_background_usage_event")
		insertCtx, cancel := context.WithTimeout(store.WithTenantID(detached, event.TenantID), backgroundUsageInsertTimeout)
		defer cancel()
		if err := events.InsertEvent(insertCtx, &event); err != nil {
			slog.Warn("usage.llm_background.record_failed",
				"purpose", event.ResourceName, "source", event.Source, "error", err)
		}
	}()
}
