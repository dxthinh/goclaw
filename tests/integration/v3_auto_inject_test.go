//go:build integration

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/memory"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

type autoInjectFixture struct {
	db       *sql.DB
	ctx      context.Context
	tenantID uuid.UUID
	agentID  uuid.UUID
	userID   string
	configs  store.SystemConfigStore
	injector memory.AutoInjector
}

// newAutoInjectFixture dựng injector trên store thật. Vector giả có giá trị dương ngẫu
// nhiên nên cosine giữa hai văn bản khác nhau khoảng 0,75, gần với số đo thực tế: vector
// đơn lẻ không vượt được ngưỡng 0,8, phải có thêm tín hiệu từ khóa.
func newAutoInjectFixture(t *testing.T) *autoInjectFixture {
	t.Helper()
	db := testDB(t)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	episodic := pg.NewPGEpisodicStore(db)
	episodic.SetEmbeddingProvider(newMockEmbedProvider())
	userID := "inject-" + tenantID.String()[:8]
	if err := episodic.Create(ctx, &store.EpisodicSummary{
		TenantID:   tenantID,
		AgentID:    agentID,
		UserID:     userID,
		SessionKey: "old-sess",
		Summary:    "User planned a Kubernetes rollback for the billing service",
		KeyTopics:  []string{"rollback"},
		L0Abstract: "Kubernetes rollback plan for billing",
		SourceType: "session",
		SourceID:   "inject-" + uuid.NewString()[:8],
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	configs := pg.NewPGSystemConfigStore(db)
	return &autoInjectFixture{
		db: db, ctx: ctx, tenantID: tenantID, agentID: agentID, userID: userID, configs: configs,
		injector: memory.NewAutoInjector(episodic, pg.NewPGEvolutionMetricsStore(db), configs),
	}
}

func (f *autoInjectFixture) inject(t *testing.T, message string) *memory.InjectResult {
	t.Helper()
	result, err := f.injector.Inject(f.ctx, memory.InjectParams{
		AgentID:       f.agentID.String(),
		UserID:        f.userID,
		TenantID:      f.tenantID.String(),
		SessionKey:    "current-sess",
		UserMessage:   message,
		RecentContext: "unrelated earlier chat about the weather",
	})
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	return result
}

// waitMetric chờ goroutine nền ghi metric retrieval/auto_inject vào bảng thật.
func (f *autoInjectFixture) waitMetric(t *testing.T) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var raw []byte
		var sessionKey string
		err := f.db.QueryRowContext(f.ctx,
			`SELECT value, session_key FROM agent_evolution_metrics
			 WHERE agent_id = $1 AND metric_type = 'retrieval' AND metric_key = 'auto_inject'`,
			f.agentID).Scan(&raw, &sessionKey)
		if err == nil {
			if sessionKey != "current-sess" {
				t.Errorf("session_key = %q, want current-sess", sessionKey)
			}
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatalf("metric value: %v", err)
			}
			return value
		}
		if time.Now().After(deadline) {
			t.Fatalf("metric not recorded: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestAutoInject_RelevantMessageIsInjected(t *testing.T) {
	f := newAutoInjectFixture(t)

	result := f.inject(t, "what was the kubernetes rollback plan again")
	if result.Section == "" {
		t.Fatalf("expected memory to be injected, top score %v", result.TopScore)
	}

	value := f.waitMetric(t)
	if value["outcome"] != "injected" || value["injected"] != float64(1) {
		t.Errorf("metric = %v, want injected", value)
	}
	if score, _ := value["top_score"].(float64); score < 0.8 {
		t.Errorf("top_score = %v, want >= 0.8", score)
	}
	if tokens, _ := value["memory_tokens"].(float64); tokens <= 0 {
		t.Errorf("memory_tokens = %v, want > 0", value["memory_tokens"])
	}
}

// Không có từ nào khớp thì điểm chỉ còn cosine, dưới ngưỡng, nhưng vẫn phải biết top_score.
func TestAutoInject_UnrelatedMessageStaysBelowThreshold(t *testing.T) {
	f := newAutoInjectFixture(t)

	result := f.inject(t, "banana spaceship orbit tomorrow morning")
	if result.Section != "" {
		t.Errorf("Section = %q, want empty", result.Section)
	}

	value := f.waitMetric(t)
	if value["outcome"] != "below_threshold" || value["injected"] != float64(0) {
		t.Errorf("metric = %v, want below_threshold", value)
	}
	if score, _ := value["top_score"].(float64); score <= 0 || score >= 0.8 {
		t.Errorf("top_score = %v, want the cosine in (0, 0.8)", score)
	}
}

func TestAutoInject_SwitchAndThresholdPerTenant(t *testing.T) {
	f := newAutoInjectFixture(t)

	if err := f.configs.Set(f.ctx, "memory.auto_inject.min_score", "0.1"); err != nil {
		t.Fatalf("Set min_score: %v", err)
	}
	if result := f.inject(t, "banana spaceship orbit tomorrow morning"); result.Section == "" {
		t.Errorf("tenant threshold 0.1 should inject, top score %v", result.TopScore)
	}

	if err := f.configs.Set(f.ctx, "memory.auto_inject.enabled", "false"); err != nil {
		t.Fatalf("Set enabled: %v", err)
	}
	result := f.inject(t, "what was the kubernetes rollback plan again")
	if result.Section != "" || result.Outcome != "disabled" || result.TopScore != 0 {
		t.Errorf("disabled result = %+v, want no search", result)
	}

	// Công tắc của tenant này không ảnh hưởng tenant khác.
	other := newAutoInjectFixture(t)
	if result := other.inject(t, "what was the kubernetes rollback plan again"); result.Outcome != "injected" {
		t.Errorf("other tenant outcome = %q, want injected", result.Outcome)
	}
}
