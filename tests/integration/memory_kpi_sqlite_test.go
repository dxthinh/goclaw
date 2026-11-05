//go:build sqliteonly

package integration

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/sqlitestore"
)

func TestMemoryKPI_SQLite(t *testing.T) {
	tmp := t.TempDir()
	stores, err := sqlitestore.NewSQLiteStores(store.StoreConfig{
		SQLitePath:       filepath.Join(tmp, "kpi.db"),
		SkillsStorageDir: filepath.Join(tmp, "skills"),
		EncryptionKey:    "test-key-32-bytes-long-for-aes!!",
	})
	if err != nil {
		t.Fatalf("NewSQLiteStores: %v", err)
	}
	t.Cleanup(func() { stores.DB.Close() })
	if stores.MemoryKPI == nil {
		t.Fatal("factory chưa nối MemoryKPI")
	}
	runMemoryKPISuite(t, &kpiHarness{
		db:  stores.DB,
		kpi: stores.MemoryKPI,
		// Cột này do strftime mặc định ghi; trace và span thì lưu định dạng time.Time của Go.
		metricTime: func(at time.Time) any { return at.UTC().Format("2006-01-02T15:04:05.000Z") },
		bucket:     func(day time.Time) any { return store.MemoryKPIDayKey(day) },
	})
}
