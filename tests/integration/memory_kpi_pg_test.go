//go:build integration

package integration

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

func TestMemoryKPI_Postgres(t *testing.T) {
	db := testDB(t)
	identity := func(at time.Time) any { return at }
	runMemoryKPISuite(t, &kpiHarness{db: db, kpi: pg.NewPGMemoryKPIStore(db), metricTime: identity, bucket: identity})
}

// Hạ rồi nâng lại 000100: bảng được tạo lại và trace cũ đã kết thúc được bù duration_ms.
func TestMemoryKPI_MigrationBackfillsTraceDuration(t *testing.T) {
	db := testDB(t)
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = defaultTestDSN
	}
	m, err := migrate.New("file://../../migrations", dsn)
	if err != nil {
		t.Fatalf("migrate.New: %v", err)
	}
	t.Cleanup(func() {
		if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			t.Errorf("khôi phục migration: %v", err)
		}
		m.Close()
	})
	if err := m.Steps(-1); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
	var exists bool
	if err := db.QueryRow(`SELECT to_regclass('memory_kpi_daily') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("down phải xóa memory_kpi_daily")
	}

	tenantID, agentID := seedTenantAgent(t, db)
	start := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	finished, running, known := uuid.New(), uuid.New(), uuid.New()
	end := start.Add(2500 * time.Millisecond)
	for _, row := range []struct {
		id       uuid.UUID
		end      any
		duration any
	}{{finished, end, nil}, {running, nil, nil}, {known, end, 123}} {
		if _, err := db.Exec(`INSERT INTO traces (id, tenant_id, agent_id, status, start_time, end_time, duration_ms, created_at)
			VALUES ($1, $2, $3, 'completed', $4, $5, $6, $4)`, row.id, tenantID, agentID, start, row.end, row.duration); err != nil {
			t.Fatalf("seed trace: %v", err)
		}
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM traces WHERE tenant_id = $1`, tenantID) })

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	duration := func(id uuid.UUID) *int {
		var got *int
		if err := db.QueryRow(`SELECT duration_ms FROM traces WHERE id = $1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := duration(finished); got == nil || *got != 2500 {
		t.Fatalf("trace đã kết thúc: duration_ms = %v, muốn 2500", got)
	}
	if got := duration(running); got != nil {
		t.Fatalf("trace chưa kết thúc: duration_ms = %v, muốn NULL", *got)
	}
	if got := duration(known); got == nil || *got != 123 {
		t.Fatalf("giá trị đã có không được đổi: %v", got)
	}
}
