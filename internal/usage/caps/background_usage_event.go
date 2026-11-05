package caps

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tracing"
)

const (
	// maxPurposeLength chặn độ dài resource_name để số giá trị luôn nhỏ.
	maxPurposeLength = 64
	// unspecifiedPurpose dùng khi caller không khai báo purpose.
	unspecifiedPurpose = "unspecified"
)

// dynamicPurposePrefixes là các tiền tố mà phần đuôi do runtime sinh ra
// (ví dụ "channel-delivery-quick_ack"). Phần đuôi bị cắt để purpose ổn định.
var dynamicPurposePrefixes = []string{"channel-delivery-"}

// NormalizePurpose rút purpose về dạng ổn định dùng làm resource_name:
// bỏ phần sau dấu ':' đầu tiên (candidatePurpose có dạng purpose:provider:model),
// bỏ hậu tố động, cắt độ dài, và trả "unspecified" khi rỗng.
func NormalizePurpose(purpose string) string {
	p := strings.TrimSpace(purpose)
	if i := strings.IndexByte(p, ':'); i >= 0 {
		p = p[:i]
	}
	for _, prefix := range dynamicPurposePrefixes {
		if strings.HasPrefix(p, prefix) {
			p = strings.TrimSuffix(prefix, "-")
			break
		}
	}
	p = strings.TrimSpace(p)
	if len(p) > maxPurposeLength {
		p = strings.ToValidUTF8(p[:maxPurposeLength], "")
	}
	if p == "" {
		return unspecifiedPurpose
	}
	return p
}

// BackgroundUsage mô tả lời gọi LLM nền cần ghi usage event.
type BackgroundUsage struct {
	// Source là store.UsageSourceUsageCaps hoặc store.UsageSourceAgentInternal.
	Source  string
	Purpose string
	// TenantID và AgentID rỗng thì lấy từ context.
	TenantID uuid.UUID
	AgentID  uuid.UUID
}

// buildBackgroundEvent dựng event chỉ từ số liệu: không đưa prompt, nội dung
// phản hồi hay user id vào event.
func buildBackgroundEvent(ctx context.Context, t *CallTrace, usage BackgroundUsage, resp *providers.ChatResponse, callErr error, now time.Time) store.UsageEvent {
	tenantID := usage.TenantID
	if tenantID == uuid.Nil {
		tenantID = store.TenantIDFromContext(ctx)
	}
	if tenantID == uuid.Nil {
		tenantID = store.MasterTenantID
	}
	agentID := usage.AgentID
	if agentID == uuid.Nil {
		agentID = store.AgentIDFromContext(ctx)
	}
	eventTime := t.started
	if eventTime.IsZero() {
		eventTime = now
	}
	eventTime = eventTime.UTC()
	purpose := NormalizePurpose(usage.Purpose)

	event := store.UsageEvent{
		ID:           uuid.New(),
		TenantID:     tenantID,
		EventTime:    eventTime,
		BucketHour:   eventTime.Truncate(time.Hour),
		EventType:    store.UsageEventTypeLLMBackground,
		ResourceType: store.UsageResourceTypeLLMPurpose,
		ResourceName: purpose,
		ResourceID:   purpose,
		Source:       usage.Source,
		AgentID:      nonNilUUID(agentID),
		TeamID:       tracing.TraceTeamIDPtrFromContext(ctx),
		Provider:     t.providerName,
		Model:        t.modelID,
		Status:       "completed",
		CallCount:    1,
	}
	if !t.started.IsZero() {
		event.DurationMS = int(now.Sub(t.started).Milliseconds())
	}
	if traceID := tracing.TraceIDFromContext(ctx); traceID != uuid.Nil {
		event.TraceID = &traceID
	}
	if rc := store.RunContextFromCtx(ctx); rc != nil {
		event.RunID = rc.RunID
		event.SessionKey = rc.SessionKey
		event.Channel = rc.Channel
		if event.TeamID == nil && rc.TeamID != "" {
			if teamID, err := uuid.Parse(rc.TeamID); err == nil {
				event.TeamID = &teamID
			}
		}
	}
	if callErr != nil {
		event.Status = "error"
		event.ErrorCount = 1
	}

	metadata := map[string]any{}
	if t.attempts > 1 {
		metadata["attempts"] = t.attempts
	}
	if resp != nil && resp.Usage != nil {
		u := resp.Usage
		event.InputTokens = int64(u.PromptTokens)
		event.OutputTokens = int64(u.CompletionTokens)
		event.TotalTokens = int64(u.TotalTokens)
		event.CacheReadTokens = int64(u.CacheReadTokens)
		event.CacheCreateTokens = int64(u.CacheCreationTokens)
		event.ThinkingTokens = int64(u.ThinkingTokens)
		if event.TotalTokens == 0 {
			event.TotalTokens = event.InputTokens + event.OutputTokens
		}
		if u.PromptTokensIncludeCachedSegments {
			metadata["prompt_tokens_include_cached_segments"] = true
		}
	}
	if len(metadata) > 0 {
		if data, err := json.Marshal(metadata); err == nil {
			event.Metadata = data
		}
	}
	return event
}

func nonNilUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}
