//go:build integration || sqliteonly

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/memoryeval"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type kpiFakeProvider struct {
	providers.Provider
	content string
	calls   chan struct{}
}

func (p *kpiFakeProvider) Chat(context.Context, providers.ChatRequest) (*providers.ChatResponse, error) {
	defer func() { p.calls <- struct{}{} }()
	return &providers.ChatResponse{Content: p.content, Usage: &providers.Usage{PromptTokens: 9, CompletionTokens: 4}}, nil
}
func (p *kpiFakeProvider) Name() string         { return "kpi-fake" }
func (p *kpiFakeProvider) DefaultModel() string { return "kpi-model" }

type kpiFakeConfigs struct {
	store.SystemConfigStore
	values map[string]string
}

func (c kpiFakeConfigs) Get(_ context.Context, key string) (string, error) {
	if v, ok := c.values[key]; ok {
		return v, nil
	}
	return "", store.ErrSystemConfigNotFound
}

func (c kpiFakeConfigs) List(context.Context) (map[string]string, error) {
	return map[string]string{"background.provider": "kpi-fake"}, nil
}

// runMemoryKPIEvaluatorCases chạy bộ chấm thật trên store thật: từ ứng viên tới dòng rollup.
func runMemoryKPIEvaluatorCases(t *testing.T, h *kpiHarness) {
	const secret = "nội dung riêng tư của cuộc trò chuyện mẫu"
	newEvaluator := func(e *kpiEnv, limit string, content string) (*memoryeval.Evaluator, *kpiFakeProvider) {
		provider := &kpiFakeProvider{content: content, calls: make(chan struct{}, 8)}
		registry := providers.NewRegistry(store.TenantIDFromContext)
		registry.RegisterForTenant(e.tenant, provider)
		return memoryeval.NewEvaluator(memoryeval.EvaluatorDeps{
			Store:         h.kpi,
			SystemConfigs: kpiFakeConfigs{values: map[string]string{"memory.eval.daily_limit": limit}},
			Registry:      registry,
		}), provider
	}
	candidate := func(e *kpiEnv) memoryeval.Candidate {
		return memoryeval.CandidatesFromRun(memoryeval.RunSnapshot{
			TenantID: e.tenant, AgentID: e.agent, TraceID: uuid.New(),
			Question: secret, Answer: secret, MemorySection: "- " + secret + "\n- mục hai",
		})[0]
	}
	countRows := func(t *testing.T, e *kpiEnv) int {
		t.Helper()
		n, err := h.kpi.CountEvalsSince(e.ctx, store.MemoryKPIDay(time.Now()))
		if err != nil {
			t.Fatalf("CountEvalsSince: %v", err)
		}
		return n
	}
	waitForRows := func(t *testing.T, e *kpiEnv, want int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for countRows(t, e) < want && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := countRows(t, e); got != want {
			t.Fatalf("số dòng = %d, muốn %d", got, want)
		}
	}

	t.Run("EvaluatorStoresOnlyNumbersThenRollsUp", func(t *testing.T) {
		e := h.newKPIEnv(t)
		// Phản hồi bị dụ: số vượt khoảng hợp lệ và lý do trích nguyên văn hội thoại.
		evaluator, _ := newEvaluator(e, "30", `{"relevant_items":999,"used":true,"reason":"`+secret+`"}`)
		evaluator.Start()
		t.Cleanup(evaluator.Stop)
		evaluator.Submit(candidate(e))
		waitForRows(t, e, 1)

		var channel, reason, provider string
		var items, relevant int
		if err := h.db.QueryRow(`SELECT channel, reason, provider, item_count, relevant_items FROM memory_eval_results WHERE tenant_id = $1`, e.tenant).
			Scan(&channel, &reason, &provider, &items, &relevant); err != nil {
			t.Fatal(err)
		}
		if channel != store.MemoryEvalChannelAutoInject || provider != "kpi-fake" || items != 2 || relevant != 2 {
			t.Fatalf("dòng sai: channel=%s provider=%s %d/%d", channel, provider, relevant, items)
		}
		if strings.Contains(reason, "riêng tư") {
			t.Fatalf("reason chứa nội dung hội thoại: %q", reason)
		}

		day := store.MemoryKPIDay(time.Now())
		if err := h.kpi.RefreshDailyRollup(e.ctx, day, false); err != nil {
			t.Fatalf("RefreshDailyRollup: %v", err)
		}
		got := e.snapshot(t, day)
		for key, want := range map[string]float64{
			"eval_samples|auto_inject": 1, "eval_items|auto_inject": 2, "eval_relevant_items|auto_inject": 2,
			"eval_used|auto_inject": 1, "eval_samples|episodic": 1, "eval_items|episodic": 2,
		} {
			if got[key] != want {
				t.Errorf("%s = %v, muốn %v", key, got[key], want)
			}
		}
		if _, ok := got["eval_samples|document"]; ok {
			t.Errorf("mẫu auto-inject không thuộc tầng document: %v", got)
		}
	})

	t.Run("EvaluatorHonorsDailyLimitOnRealCount", func(t *testing.T) {
		e := h.newKPIEnv(t)
		evaluator, provider := newEvaluator(e, "1", `{"relevant_items":1,"used":false,"reason":"phù hợp một phần"}`)
		evaluator.Start()
		evaluator.Submit(candidate(e))
		waitForRows(t, e, 1)
		evaluator.Submit(candidate(e))
		evaluator.Submit(candidate(e))
		// Chờ hàng đợi cạn: khi cả hai mẫu sau đã bị từ chối thì không còn lời gọi nào tới.
		time.Sleep(200 * time.Millisecond)
		evaluator.Stop()
		if calls := len(provider.calls); calls != 1 {
			t.Fatalf("calls = %d, muốn 1", calls)
		}
		if got := countRows(t, e); got != 1 {
			t.Fatalf("số dòng = %d, muốn 1", got)
		}
	})
}
