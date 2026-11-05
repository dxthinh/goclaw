package agent

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	usagecaps "github.com/nextlevelbuilder/goclaw/internal/usage/caps"
)

func (l *Loop) reserveInternalLLMUsage(ctx context.Context, chatReq providers.ChatRequest, purpose string) (*usagecaps.Reservation, error) {
	if l.usageCaps == nil || l.provider == nil {
		return nil, nil
	}
	return l.reserveInternalLLMUsageFor(ctx, chatReq, purpose, l.provider.Name(), chatReq.Model)
}

func (l *Loop) reserveInternalLLMUsageFor(ctx context.Context, chatReq providers.ChatRequest, purpose, providerName, model string) (*usagecaps.Reservation, error) {
	if l.usageCaps == nil {
		return nil, nil
	}
	if model == "" {
		model = chatReq.Model
	}
	if model == "" {
		model = l.model
	}
	tenantID := store.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		tenantID = l.tenantID
	}
	return l.usageCaps.Preflight(ctx, usagecaps.Request{
		TenantID:        tenantID,
		AgentID:         l.agentUUID,
		ProviderName:    providerName,
		ModelID:         model,
		ReservationKey:  fmt.Sprintf("%s:%s:%s", purpose, l.agentUUID.String(), uuid.NewString()),
		Messages:        chatReq.Messages,
		MaxOutputTokens: l.maxOutputTokensFromRequest(chatReq),
	})
}

// callInternalLLMWithUsage gọi LLM cho việc nội bộ của agent (compaction, memory
// flush) với guard cửa sổ ngữ cảnh và reservation. Lời gọi không đi qua
// usagecaps.Service.Chat nên tự ghi đúng một usage event nền (agent_internal)
// cho mỗi lời gọi, kể cả khi fallback qua nhiều candidate.
func (l *Loop) callInternalLLMWithUsage(ctx context.Context, chatReq providers.ChatRequest, purpose string) (*providers.ChatResponse, error) {
	var trace usagecaps.CallTrace
	if fallbackProvider, ok := l.provider.(*providers.ModelFallbackProvider); ok {
		before := func(callCtx context.Context, entry providers.FallbackCandidate, actualReq providers.ChatRequest) (providers.FallbackAfterCall, error) {
			candidatePurpose := fmt.Sprintf("%s:%s:%s", purpose, entry.ProviderName, actualReq.Model)
			// Mandatory hard-ceiling guard: internal calls (compaction, session
			// summarization, memory flush) obey the SAME per-agent window as
			// main-loop calls, per candidate, after all request mutations.
			if guardErr := l.guardCompleteModelRequest(actualReq, entry.ProviderName, actualReq.Model, candidatePurpose); guardErr != nil {
				return nil, guardErr
			}
			reservation, err := l.reserveInternalLLMUsageFor(callCtx, actualReq, candidatePurpose, entry.ProviderName, actualReq.Model)
			if err != nil {
				return nil, err
			}
			trace.Begin(entry.ProviderName, actualReq.Model)
			return func(resp *providers.ChatResponse, callErr error, _ providers.FallbackCallInfo) {
				trace.End(resp)
				if reservation != nil {
					reservation.Reconcile(callCtx, resp, callErr)
				}
			}, nil
		}
		resp, err := fallbackProvider.ChatWithHook(ctx, chatReq, before)
		l.recordInternalLLMUsage(ctx, &trace, purpose, resp, err)
		return resp, err
	}
	providerName := ""
	if l.provider != nil {
		providerName = l.provider.Name()
	}
	callModel := chatReq.Model
	if callModel == "" {
		callModel = l.model
	}
	// Mandatory hard-ceiling guard for the non-fallback internal call path.
	if guardErr := l.guardCompleteModelRequest(chatReq, providerName, callModel, purpose); guardErr != nil {
		return nil, guardErr
	}
	reservation, reserveErr := l.reserveInternalLLMUsage(ctx, chatReq, purpose)
	if reserveErr != nil {
		return nil, reserveErr
	}
	trace.Begin(providerName, callModel)
	resp, err := l.provider.Chat(ctx, chatReq)
	trace.End(resp)
	if reservation != nil {
		reservation.Reconcile(ctx, resp, err)
	}
	l.recordInternalLLMUsage(ctx, &trace, purpose, resp, err)
	return resp, err
}

// recordInternalLLMUsage ghi usage event nền cho một lời gọi nội bộ đã chạm tới
// provider. Không làm gì khi agent chưa có event store hoặc lời gọi bị chặn
// trước khi tới provider.
func (l *Loop) recordInternalLLMUsage(ctx context.Context, trace *usagecaps.CallTrace, purpose string, resp *providers.ChatResponse, callErr error) {
	if l.usageEvents == nil {
		return
	}
	tenantID := store.TenantIDFromContext(ctx)
	if tenantID == uuid.Nil {
		tenantID = l.tenantID
	}
	trace.Record(ctx, l.usageEvents, usagecaps.BackgroundUsage{
		Source:   store.UsageSourceAgentInternal,
		Purpose:  purpose,
		TenantID: tenantID,
		AgentID:  l.agentUUID,
	}, resp, callErr)
}

func (l *Loop) maxOutputTokensFromRequest(chatReq providers.ChatRequest) int {
	maxTokens := l.effectiveMaxTokens()
	if chatReq.Options == nil {
		return maxTokens
	}
	if v, ok := chatReq.Options[providers.OptMaxTokens]; ok {
		switch n := v.(type) {
		case int:
			maxTokens = n
		case int64:
			maxTokens = int(n)
		case float64:
			maxTokens = int(n)
		}
	}
	return maxTokens
}
