//go:build sqliteonly

package integration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/sqlitestore"
)

func TestSQLiteChildTraceCosts(t *testing.T) {
	stores, err := sqlitestore.NewSQLiteStores(store.StoreConfig{
		SQLitePath: filepath.Join(t.TempDir(), "cost.db"), SkillsStorageDir: t.TempDir(),
		EncryptionKey: "test-key-32-bytes-long-for-aes!!",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stores.DB.Close()
	tenant, agent := uuid.New(), uuid.New()
	if _, err = stores.DB.Exec(`INSERT INTO tenants (id,name,slug,status) VALUES (?, 'Costs', ?, 'active')`, tenant, tenant.String()); err != nil {
		t.Fatal(err)
	}
	if _, err = stores.DB.Exec(`INSERT INTO agents (id,agent_key,display_name,status,tenant_id,owner_id,model,provider) VALUES (?,?,'Costs','active',?,'owner','model','provider')`, agent, agent.String(), tenant); err != nil {
		t.Fatal(err)
	}
	checkChildTraceCosts(t, sqlitestore.NewSQLiteTracingStore(stores.DB), store.WithTenantID(context.Background(), tenant), agent)
}
