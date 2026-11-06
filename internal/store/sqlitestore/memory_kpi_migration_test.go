//go:build sqlite || sqliteonly

package sqlitestore

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func kpiTableSQL(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&ddl); err != nil {
		t.Fatalf("đọc định nghĩa %s: %v", name, err)
	}
	return strings.Join(strings.Fields(ddl), " ")
}

// DB cũ nâng lên phải có bảng giống hệt DB mới tạo từ schema.sql, và trace cũ đã kết thúc
// được bù duration_ms. Thiếu patch này bản desktop sẽ lỗi khi khởi động.
func TestMemoryKPIMigration_FromPreviousVersion(t *testing.T) {
	db, tenantID, agentID := newAgentUpdateTestFixture(t)
	wantEval, wantDaily := kpiTableSQL(t, db, "memory_eval_results"), kpiTableSQL(t, db, "memory_kpi_daily")

	if _, err := db.Exec(`DROP TABLE memory_kpi_daily;
		DROP TABLE memory_eval_results;
		DROP INDEX idx_usage_events_llm_background_time;
		UPDATE schema_version SET version = 60;`); err != nil {
		t.Fatalf("hạ phiên bản: %v", err)
	}

	tracing := NewSQLiteTracingStore(db)
	ctx := sqliteTenantCtx(tenantID)
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	end := start.Add(2500 * time.Millisecond)
	finished, running := uuid.New(), uuid.New()
	for id, endTime := range map[uuid.UUID]*time.Time{finished: &end, running: nil} {
		if err := tracing.CreateTrace(ctx, &store.TraceData{
			ID: id, AgentID: &agentID, Status: store.TraceStatusCompleted,
			StartTime: start, EndTime: endTime, CreatedAt: start, Name: "kpi",
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	var version int
	if err := db.QueryRow(`SELECT version FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Fatalf("version = %d, muốn %d", version, SchemaVersion)
	}
	if got := kpiTableSQL(t, db, "memory_eval_results"); got != wantEval {
		t.Fatalf("memory_eval_results lệch schema.sql:\n%s\n%s", got, wantEval)
	}
	if got := kpiTableSQL(t, db, "memory_kpi_daily"); got != wantDaily {
		t.Fatalf("memory_kpi_daily lệch schema.sql:\n%s\n%s", got, wantDaily)
	}

	var duration sql.NullInt64
	if err := db.QueryRow(`SELECT duration_ms FROM traces WHERE id = ?`, finished).Scan(&duration); err != nil {
		t.Fatal(err)
	}
	// julianday là số thực nên cho phép lệch 2 ms.
	if !duration.Valid || duration.Int64 < 2498 || duration.Int64 > 2502 {
		t.Fatalf("duration_ms = %+v, muốn khoảng 2500", duration)
	}
	if err := db.QueryRow(`SELECT duration_ms FROM traces WHERE id = ?`, running).Scan(&duration); err != nil {
		t.Fatal(err)
	}
	if duration.Valid {
		t.Fatalf("trace chưa kết thúc không được có duration_ms: %+v", duration)
	}
}
