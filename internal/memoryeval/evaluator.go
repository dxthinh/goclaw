package memoryeval

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/providerresolve"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/safego"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	usagecaps "github.com/nextlevelbuilder/goclaw/internal/usage/caps"
)

const (
	configEvalEnabled    = "memory.eval.enabled"
	configEvalDailyLimit = "memory.eval.daily_limit"
	defaultDailyLimit    = 30

	evalQueueSize   = 32
	evalCallTimeout = 60 * time.Second
	evalPurpose     = "memory-eval"
	// evalContextWindow đủ cho prompt đã bị giới hạn độ dài. Khai báo tường minh vì lời gọi
	// có agent mà thiếu ngân sách sẽ ghi cảnh báo sai lệch ở mỗi mẫu.
	evalContextWindow = 32000
)

// EvaluatorDeps là các phụ thuộc của Evaluator. UsageCaps nil trên Lite vẫn gọi được.
type EvaluatorDeps struct {
	Store         store.MemoryKPIStore
	SystemConfigs store.SystemConfigStore
	Registry      *providers.Registry
	UsageCaps     *usagecaps.Service
}

// Evaluator chấm các ứng viên tuần tự trên một goroutine nền.
type Evaluator struct {
	lifecycle
	deps     EvaluatorDeps
	queue    chan Candidate
	now      func() time.Time
	failures map[uuid.UUID]failureCount // chỉ goroutine worker truy cập
}

// NewEvaluator tạo Evaluator, chưa chạy cho tới khi gọi Start.
func NewEvaluator(deps EvaluatorDeps) *Evaluator {
	return &Evaluator{
		lifecycle: newLifecycle(),
		deps:      deps,
		queue:     make(chan Candidate, evalQueueSize),
		now:       time.Now,
		failures:  make(map[uuid.UUID]failureCount),
	}
}

// Start chạy worker. Gọi nhiều lần chỉ có tác dụng một lần.
func (e *Evaluator) Start() {
	e.start(func(ctx context.Context) {
		for {
			select {
			case <-ctx.Done():
				return
			case c := <-e.queue:
				e.process(ctx, c)
			}
		}
	})
}

// Submit đưa ứng viên vào hàng đợi, không bao giờ chặn luồng trả lời. Đầy thì bỏ mẫu mới và
// trả false; nil receiver là no-op.
func (e *Evaluator) Submit(c Candidate) bool {
	if e == nil {
		return false
	}
	select {
	case e.queue <- c:
		return true
	default:
		slog.Debug("memory.eval.queue_full", "agent", c.AgentID, "channel", c.Channel)
		return false
	}
}

// process chấm một ứng viên. Mọi lỗi chỉ log; không ghi nội dung hội thoại vào log.
func (e *Evaluator) process(ctx context.Context, c Candidate) {
	defer safego.Recover(nil, "worker", "memory_eval")
	tctx := store.WithTenantID(ctx, c.TenantID)
	if !e.enabled(tctx) || !e.withinDailyLimit(tctx, c.TenantID) {
		return
	}
	provider, model := providerresolve.ResolveBackgroundProvider(tctx, c.TenantID, e.deps.Registry, e.deps.SystemConfigs)
	if provider == nil {
		slog.Debug("memory.eval.no_provider", "tenant", c.TenantID)
		return
	}
	req, err := buildScoringRequest(c, model)
	if err != nil {
		slog.Warn("memory.eval.build_failed", "agent", c.AgentID, "error", err)
		return
	}

	callCtx, cancel := context.WithTimeout(tctx, evalCallTimeout)
	defer cancel()
	resp, err := e.deps.UsageCaps.Chat(callCtx, provider, req, usagecaps.ChatOptions{
		TenantID:           c.TenantID,
		AgentID:            c.AgentID,
		ProviderName:       provider.Name(),
		ModelID:            model,
		Purpose:            evalPurpose,
		MaxOutputTokens:    maxOutputTokens,
		AgentContextWindow: evalContextWindow,
		AgentMaxTokens:     maxOutputTokens,
	})
	if err != nil {
		e.recordFailure(c.TenantID)
		slog.Warn("memory.eval.call_failed", "agent", c.AgentID, "channel", c.Channel, "error", err)
		return
	}
	sources := append([]string{c.Question, c.Answer}, c.Items...)
	result, err := parseScore(resp.Content, len(c.Items), sources...)
	if err != nil {
		e.recordFailure(c.TenantID)
		slog.Warn("memory.eval.bad_response", "agent", c.AgentID, "channel", c.Channel, "error", err)
		return
	}
	if err := e.deps.Store.InsertEvalResult(tctx, newEvalResult(c, result, provider.Name(), model, resp)); err != nil {
		e.recordFailure(c.TenantID)
		slog.Warn("memory.eval.insert_failed", "agent", c.AgentID, "error", err)
	}
}

// newEvalResult chỉ sao chép số liệu và lý do đã làm sạch, không mang theo nội dung hội thoại.
func newEvalResult(c Candidate, s score, providerName, model string, resp *providers.ChatResponse) store.MemoryEvalResult {
	result := store.MemoryEvalResult{
		AgentID:           c.AgentID,
		Channel:           c.Channel,
		HasEpisodic:       c.HasEpisodic,
		HasDocument:       c.HasDocument,
		HasKnowledgeGraph: c.HasKnowledgeGraph,
		ItemCount:         len(c.Items),
		RelevantItems:     s.RelevantItems,
		Used:              s.Used,
		Reason:            s.Reason,
		Provider:          providerName,
		Model:             model,
	}
	if c.TraceID != uuid.Nil {
		traceID := c.TraceID
		result.TraceID = &traceID
	}
	if resp.Usage != nil {
		result.InputTokens = int64(resp.Usage.PromptTokens)
		result.OutputTokens = int64(resp.Usage.CompletionTokens)
	}
	return result
}
