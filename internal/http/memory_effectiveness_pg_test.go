//go:build integration

package http

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

// Dùng store PG thật: hai tenant cùng có dữ liệu trong memory_kpi_daily, mỗi tenant chỉ thấy
// số của mình và ctx không có tenant thì không nhận được số nào.
func TestMemoryEffectiveness_PostgresTenantIsolation(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL chưa đặt")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	today := store.MemoryKPIDay(time.Now())
	tenantA, tenantB := uuid.New(), uuid.New()
	for i, tenant := range []uuid.UUID{tenantA, tenantB} {
		slug := "eff" + tenant.String()[:8]
		if _, err := db.Exec(`INSERT INTO tenants (id, name, slug, status) VALUES ($1, $2, $3, 'active')`, tenant, slug, slug); err != nil {
			t.Fatalf("insert tenant: %v", err)
		}
		tenant := tenant
		t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM tenants WHERE id = $1`, tenant) }) // cascade xóa memory_kpi_daily
		runs := float64(10 * (i + 1))                                                     // A=10, B=20
		if _, err := db.Exec(`INSERT INTO memory_kpi_daily (tenant_id, bucket_date, agent_id, metric, dimension, value)
			VALUES ($1, $2, $3, 'prompt_runs', '', $4)`, tenant, today, kpiAgentA, runs); err != nil {
			t.Fatalf("insert kpi: %v", err)
		}
	}

	h := NewMemoryEffectivenessHandler(pg.NewPGMemoryKPIStore(db), pg.NewPGSystemConfigStore(db))
	get := func(tenant uuid.UUID) (int, effectivenessResponse) {
		req := httptest.NewRequest(http.MethodGet, "/v1/memory/effectiveness?days=7", nil)
		if tenant != uuid.Nil {
			req = req.WithContext(store.WithTenantID(req.Context(), tenant))
		}
		rec := httptest.NewRecorder()
		h.handleGet(rec, req)
		var resp effectivenessResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec.Code, resp
	}

	if code, resp := get(tenantA); code != http.StatusOK || resp.Prompt.Runs != 10 {
		t.Fatalf("tenant A: status=%d runs=%d, want 200/10", code, resp.Prompt.Runs)
	}
	if code, resp := get(tenantB); code != http.StatusOK || resp.Prompt.Runs != 20 {
		t.Fatalf("tenant B: status=%d runs=%d, want 200/20", code, resp.Prompt.Runs)
	}
	if code, resp := get(uuid.Nil); code != http.StatusForbidden || resp.Prompt.Runs != 0 || len(resp.Funnel) != 0 {
		t.Fatalf("không tenant: status=%d resp=%+v", code, resp)
	}
}
