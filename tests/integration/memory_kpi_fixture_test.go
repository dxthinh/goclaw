//go:build integration || sqliteonly

package integration

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// kpiDay là ngày UTC của fixture, đủ xa để không trùng dữ liệu thật.
var kpiDay = time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)

// kpiHarness gom phần khác nhau giữa hai dialect. SQL fixture dùng $n vì cả PostgreSQL và
// driver SQLite đều hiểu; metricTime trả đúng định dạng created_at mà mỗi dialect thực sự lưu.
type kpiHarness struct {
	db         *sql.DB
	kpi        store.MemoryKPIStore
	metricTime func(time.Time) any
	bucket     func(time.Time) any
}

type kpiEnv struct {
	h      *kpiHarness
	tenant uuid.UUID
	agent  uuid.UUID
	ctx    context.Context
}

func (e *kpiEnv) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := e.h.db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// newKPIEnv tạo tenant và agent riêng cho từng test, rồi dọn sạch mọi dòng của tenant đó.
func (h *kpiHarness) newKPIEnv(t *testing.T) *kpiEnv {
	t.Helper()
	e := &kpiEnv{h: h, tenant: uuid.New(), agent: uuid.New()}
	e.ctx = store.WithTenantID(context.Background(), e.tenant)
	e.exec(t, `INSERT INTO tenants (id, name, slug, status) VALUES ($1, $2, $3, 'active')`,
		e.tenant, "kpi-"+e.tenant.String()[:8], "kpi"+e.tenant.String()[:8])
	e.exec(t, `INSERT INTO agents (id, tenant_id, agent_key, agent_type, status, provider, model, owner_id)
		VALUES ($1, $2, $3, 'predefined', 'active', 'test', 'test-model', 'test-owner')`,
		e.agent, e.tenant, "kpi-"+e.agent.String()[:8])
	t.Cleanup(func() {
		for _, table := range []string{
			"memory_kpi_daily", "memory_eval_results", "agent_evolution_metrics", "usage_events", "spans", "traces",
			"episodic_summaries", "sessions", "kg_relations", "kg_entities", "memory_documents", "agents",
		} {
			if _, err := h.db.Exec(`DELETE FROM `+table+` WHERE tenant_id = $1`, e.tenant); err != nil {
				t.Logf("cleanup %s: %v", table, err)
			}
		}
		if _, err := h.db.Exec(`DELETE FROM tenants WHERE id = $1`, e.tenant); err != nil {
			t.Logf("cleanup tenants: %v", err)
		}
	})
	return e
}

func (e *kpiEnv) metric(t *testing.T, metricType, metricKey, value string, at time.Time) {
	t.Helper()
	e.exec(t, `INSERT INTO agent_evolution_metrics (id, tenant_id, agent_id, session_key, metric_type, metric_key, value, created_at)
		VALUES ($1, $2, $3, 'kpi-session', $4, $5, $6, $7)`,
		uuid.New(), e.tenant, e.agent, metricType, metricKey, value, e.h.metricTime(at))
}

func (e *kpiEnv) trace(t *testing.T, parent any, at time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	e.exec(t, `INSERT INTO traces (id, parent_trace_id, tenant_id, agent_id, status, start_time, created_at)
		VALUES ($1, $2, $3, $4, 'completed', $5, $6)`, id, parent, e.tenant, e.agent, at, at)
	return id
}

func (e *kpiEnv) span(t *testing.T, traceID uuid.UUID, spanType, provider, model string, input int, metadata any, at time.Time) {
	t.Helper()
	e.exec(t, `INSERT INTO spans (id, trace_id, tenant_id, agent_id, span_type, provider, model, input_tokens, metadata, start_time, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		uuid.New(), traceID, e.tenant, e.agent, spanType, provider, model, input, metadata, at, at)
}

func (e *kpiEnv) background(t *testing.T, purpose string, agent any, input, output int, at time.Time) {
	t.Helper()
	e.exec(t, `INSERT INTO usage_events (id, tenant_id, event_time, bucket_hour, event_type, resource_type, resource_name, agent_id, input_tokens, output_tokens, call_count)
		VALUES ($1, $2, $3, $4, 'llm_background', 'llm_purpose', $5, $6, $7, $8, 1)`,
		uuid.New(), e.tenant, at, at.Truncate(time.Hour), purpose, agent, input, output)
}

// seedCumulative ghi nguồn của mọi nhóm cộng dồn. Dòng ngoài ngày nằm sát hai biên để bắt lỗi
// so sánh thời gian trên từng định dạng lưu.
func (e *kpiEnv) seedCumulative(t *testing.T) {
	t.Helper()
	at := kpiDay.Add(10 * time.Hour)
	before, after := kpiDay.Add(-time.Millisecond), kpiDay.AddDate(0, 0, 1)

	e.metric(t, "retrieval", "auto_inject", `{"outcome":"injected"}`, kpiDay) // đúng biên đầu ngày
	e.metric(t, "retrieval", "auto_inject", `{"outcome":"injected"}`, at)
	e.metric(t, "retrieval", "auto_inject", `{"outcome":"no_match"}`, at)
	e.metric(t, "retrieval", "auto_inject", `{"result_count":3,"injected":2,"used_in_reply":true}`, at) // dòng cũ chỉ ghi khi đã chèn
	e.metric(t, "retrieval", "auto_inject", `{"outcome":"injected"}`, before)
	e.metric(t, "retrieval", "auto_inject", `{"outcome":"injected"}`, after)
	e.metric(t, "retrieval", "memory_search", `{"outcome":"matched"}`, at)
	e.metric(t, "retrieval", "memory_search", `{"used_in_reply":true}`, at) // dòng cũ không có outcome
	e.metric(t, "tool", "knowledge_graph_search", `{"success":true,"outcome":"matched"}`, at)
	e.metric(t, "tool", "knowledge_graph_search", `{"success":false,"outcome":"error"}`, at)
	e.metric(t, "tool", "knowledge_graph_search", `{"success":true}`, at) // dòng cũ không có outcome
	e.metric(t, "tool", "web_search", `{"success":true}`, at)

	e.metric(t, "prompt", "shape", `{"system_tokens":100,"tool_schema_tokens":20,"history_tokens":300,"tool_result_tokens":40,"memory_tokens":5}`, at)
	e.metric(t, "prompt", "shape", `{"system_tokens":200,"tool_schema_tokens":30,"history_tokens":100,"tool_result_tokens":60,"memory_tokens":15}`, at)
	e.metric(t, "prompt", "shape", `{"system_tokens":999}`, before)

	for _, r := range []store.MemoryEvalResult{
		{AgentID: e.agent, Channel: store.MemoryEvalChannelAutoInject, HasEpisodic: true, ItemCount: 4, RelevantItems: 3, Used: true, CreatedAt: at},
		{AgentID: e.agent, Channel: store.MemoryEvalChannelMemorySearch, HasEpisodic: true, HasDocument: true, ItemCount: 2, RelevantItems: 1, CreatedAt: at},
		{AgentID: e.agent, Channel: store.MemoryEvalChannelKnowledgeGraph, HasKnowledgeGraph: true, ItemCount: 5, RelevantItems: 5, Used: true, CreatedAt: at},
		{AgentID: e.agent, Channel: store.MemoryEvalChannelAutoInject, HasEpisodic: true, ItemCount: 9, RelevantItems: 9, Used: true, CreatedAt: after},
	} {
		if err := e.h.kpi.InsertEvalResult(e.ctx, r); err != nil {
			t.Fatalf("InsertEvalResult: %v", err)
		}
	}

	root := e.trace(t, nil, at)
	child := e.trace(t, root, at)
	e.span(t, root, "llm_call", "anthropic", "m1", 1000, `{"cache_read_tokens":400,"cache_creation_tokens":100}`, at)
	e.span(t, root, "llm_call", "openai", "m2", 1000, `{"cache_read_tokens":600,"prompt_tokens_include_cached_segments":true}`, at)
	e.span(t, root, "llm_call", "plain", "m3", 500, nil, at)
	e.span(t, root, "llm_call", "anthropic", "m4", 300, `{"cache_creation_tokens":300}`, at) // có báo cache nhưng trượt
	e.span(t, child, "llm_call", "anthropic", "m1", 200, `{"cache_read_tokens":50}`, at)
	e.span(t, root, "tool_call", "anthropic", "m1", 7777, nil, at)
	e.span(t, root, "llm_call", "anthropic", "m1", 8888, nil, before)

	e.background(t, "episodic-summary", e.agent, 10, 1, at)
	e.background(t, "episodic-summary", e.agent, 20, 2, at)
	e.background(t, "memory-eval", nil, 5, 7, at)
	e.background(t, "memory-eval", nil, 500, 700, after)
}

// seedGauges ghi trạng thái hiện tại của pipeline memory. Session đầu có 21 tin nhắn và bản tóm
// tắt phủ xa nhất là 11 lượt nên cần tóm tắt lại (21 >= 11+10), và chỉ tính một lần dù có hai dòng
// nguồn session. Session sau có 20 tin nhắn thì chưa cần. Dòng không phải nguồn session không ảnh
// hưởng đến phép so sánh này.
func (e *kpiEnv) seedGauges(t *testing.T) {
	t.Helper()
	at := kpiDay.Add(10 * time.Hour)
	staleKey, freshKey := "kpi-s-"+e.tenant.String(), "kpi-f-"+e.tenant.String()
	for key, count := range map[string]int{staleKey: 21, freshKey: 20} {
		messages := "[" + strings.TrimSuffix(strings.Repeat(`{"role":"user"},`, count), ",") + "]"
		e.exec(t, `INSERT INTO sessions (id, session_key, agent_id, tenant_id, messages) VALUES ($1, $2, $3, $4, $5)`,
			uuid.New(), key, e.agent, e.tenant, messages)
	}
	for _, ep := range []struct {
		key      string
		source   string
		turns    int
		promoted any
	}{{staleKey, "session", 10, at}, {staleKey, "session", 11, nil}, {staleKey, "v2-migration", 0, nil}, {freshKey, "session", 11, nil}} {
		e.exec(t, `INSERT INTO episodic_summaries (id, tenant_id, agent_id, user_id, session_key, summary, source_type, turn_count, promoted_at)
			VALUES ($1, $2, $3, '', $4, 'kpi', $5, $6, $7)`,
			uuid.New(), e.tenant, e.agent, ep.key, ep.source, ep.turns, ep.promoted)
	}
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for i, validUntil := range []any{nil, nil, at} {
		e.exec(t, `INSERT INTO kg_entities (id, tenant_id, agent_id, user_id, external_id, name, entity_type, valid_until)
			VALUES ($1, $2, $3, '', $4, 'kpi', 'kpi', $5)`, ids[i], e.tenant, e.agent, ids[i].String(), validUntil)
	}
	e.exec(t, `INSERT INTO kg_relations (id, tenant_id, agent_id, user_id, source_entity_id, relation_type, target_entity_id)
		VALUES ($1, $2, $3, '', $4, 'rel', $5)`, uuid.New(), e.tenant, e.agent, ids[0], ids[1])
	for _, path := range []string{"notes.md", "_system/dreaming/20261015-consolidated.md", "_systemx/dreaming/khong-tinh.md"} {
		e.document(t, path)
	}
}

func (e *kpiEnv) document(t *testing.T, path string) {
	t.Helper()
	e.exec(t, `INSERT INTO memory_documents (id, tenant_id, agent_id, path, content, hash) VALUES ($1, $2, $3, $4, '', 'h')`,
		uuid.New(), e.tenant, e.agent, path)
}
