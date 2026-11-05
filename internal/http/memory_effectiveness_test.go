package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// fakeKPIStore giả lập QueryDaily: fail-closed khi thiếu tenant như store thật, và chỉ trả dòng
// của tenant trong ctx.
type fakeKPIStore struct {
	store.MemoryKPIStore
	rows     []store.MemoryKPIDaily
	calls    int
	gotFrom  time.Time
	gotTo    time.Time
	gotAgent *uuid.UUID
}

func (f *fakeKPIStore) QueryDaily(ctx context.Context, agentID *uuid.UUID, from, to time.Time) ([]store.MemoryKPIDaily, error) {
	f.calls++
	tenant := store.TenantIDFromContext(ctx)
	if tenant == uuid.Nil {
		return nil, store.ErrMemoryKPITenantRequired
	}
	f.gotFrom, f.gotTo, f.gotAgent = from, to, agentID
	var out []store.MemoryKPIDaily
	for _, r := range f.rows {
		if r.TenantID == tenant {
			out = append(out, r)
		}
	}
	return out, nil
}

type fakeConfigStore struct {
	store.SystemConfigStore
	values map[string]string
}

func (f *fakeConfigStore) Get(_ context.Context, key string) (string, error) {
	if v, ok := f.values[key]; ok {
		return v, nil
	}
	return "", store.ErrSystemConfigNotFound
}

func newEffectivenessMux(t *testing.T, kpi store.MemoryKPIStore, cfg store.SystemConfigStore) *http.ServeMux {
	t.Helper()
	setupTestToken(t, "")
	setupTestNoAuthFallback(t, true)
	h := NewMemoryEffectivenessHandler(kpi, cfg)
	h.now = func() time.Time { return time.Date(2026, 11, 7, 23, 30, 0, 0, time.UTC) }
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func getEffectiveness(mux *http.ServeMux, query string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/memory/effectiveness"+query, nil))
	return rec
}

func TestMemoryEffectiveness_InvalidParamsReturn400(t *testing.T) {
	kpi := &fakeKPIStore{}
	mux := newEffectivenessMux(t, kpi, nil)
	for _, q := range []string{"?days=5", "?days=abc", "?days=0", "?days=-7", "?agent_id=not-a-uuid"} {
		if rec := getEffectiveness(mux, q); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, rec.Code)
		}
	}
	if kpi.calls != 0 {
		t.Fatalf("store bị gọi %d lần dù tham số sai", kpi.calls)
	}
}

func TestMemoryEffectiveness_DefaultWindowIsSevenUTCDaysIncludingToday(t *testing.T) {
	kpi := &fakeKPIStore{}
	rec := getEffectiveness(newEffectivenessMux(t, kpi, nil), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var resp effectivenessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Window != (effectivenessWindow{From: "2026-11-01", To: "2026-11-07", Days: 7}) {
		t.Fatalf("window = %+v", resp.Window)
	}
	if resp.Thresholds != defaultEffectivenessThresholds || !resp.Switches.AutoInjectEnabled || !resp.Switches.EvalEnabled {
		t.Fatalf("mặc định sai: %+v %+v", resp.Thresholds, resp.Switches)
	}
	if kpi.gotAgent != nil {
		t.Fatalf("agent filter = %v, want nil", kpi.gotAgent)
	}

	agent := uuid.New()
	rec = getEffectiveness(newEffectivenessMux(t, kpi, nil), "?days=90&agent_id="+agent.String())
	if rec.Code != http.StatusOK || kpi.gotAgent == nil || *kpi.gotAgent != agent || kpi.gotFrom.Format(time.DateOnly) != "2026-08-10" {
		t.Fatalf("status=%d agent=%v from=%v", rec.Code, kpi.gotAgent, kpi.gotFrom)
	}
}

func TestMemoryEffectiveness_ConfigOverridesAndFallbacks(t *testing.T) {
	cfg := &fakeConfigStore{values: map[string]string{
		cfgKPIMinSamples:     "50",
		cfgKPIKeepRelevance:  "0.9",
		cfgKPIKeepUsed:       "not-a-number", // hỏng -> mặc định
		cfgKPIOffRelevance:   "1.5",          // ngoài [0,1] -> mặc định
		cfgAutoInjectEnabled: "false",
		cfgEvalEnabled:       "garbage", // hỏng -> bật
	}}
	rec := getEffectiveness(newEffectivenessMux(t, &fakeKPIStore{}, cfg), "")
	var resp effectivenessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	want := effectivenessThresholds{MinSamples: 50, KeepRelevance: 0.9, KeepUsed: 0.3, OffRelevance: 0.4}
	if resp.Thresholds != want {
		t.Fatalf("thresholds = %+v, want %+v", resp.Thresholds, want)
	}
	if resp.Switches.AutoInjectEnabled || !resp.Switches.EvalEnabled {
		t.Fatalf("switches = %+v", resp.Switches)
	}
}

// Không có tenant trong ctx thì store từ chối và phản hồi không chứa số liệu nào.
func TestMemoryEffectiveness_NoTenantReturnsNoData(t *testing.T) {
	tenant := uuid.New()
	kpi := &fakeKPIStore{rows: []store.MemoryKPIDaily{{TenantID: tenant, BucketDate: time.Now(), AgentID: kpiAgentA, Metric: "prompt_runs", Value: 99}}}
	h := NewMemoryEffectivenessHandler(kpi, nil)
	rec := httptest.NewRecorder()
	h.handleGet(rec, httptest.NewRequest(http.MethodGet, "/v1/memory/effectiveness", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "funnel") || strings.Contains(body, "99") {
		t.Fatalf("phản hồi lộ số liệu: %s", body)
	}
}

func TestMemoryEffectiveness_OtherTenantSeesNothing(t *testing.T) {
	owner, other := uuid.New(), uuid.New()
	kpi := &fakeKPIStore{rows: []store.MemoryKPIDaily{
		{TenantID: owner, BucketDate: time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), AgentID: kpiAgentA, Metric: "prompt_runs", Value: 7},
	}}
	h := NewMemoryEffectivenessHandler(kpi, nil)
	h.now = func() time.Time { return time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC) }
	runs := func(tenant uuid.UUID) int64 {
		req := httptest.NewRequest(http.MethodGet, "/v1/memory/effectiveness", nil)
		req = req.WithContext(store.WithTenantID(req.Context(), tenant))
		rec := httptest.NewRecorder()
		h.handleGet(rec, req)
		var resp effectivenessResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.Prompt.Runs
	}
	if got := runs(owner); got != 7 {
		t.Fatalf("owner thấy %d, want 7", got)
	}
	if got := runs(other); got != 0 {
		t.Fatalf("tenant khác thấy %d, want 0", got)
	}
}
