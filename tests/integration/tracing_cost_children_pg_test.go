//go:build integration

package integration

import (
	"context"
	"os"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

func TestPostgresChildTraceCosts(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("explicit disposable TEST_DATABASE_URL required")
	}
	db := testDB(t)
	tenant, agent := seedTenantAgent(t, db)
	checkChildTraceCosts(t, pg.NewPGTracingStore(db), store.WithTenantID(context.Background(), tenant), agent)
}
