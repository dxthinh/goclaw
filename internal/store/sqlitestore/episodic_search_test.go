//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func TestSQLiteEpisodicSearch_TimeFiltersAndExpiry(t *testing.T) {
	db := openTestDB(t)
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	s := NewSQLiteEpisodicStore(db)
	tenantID := uuid.New()
	agentID := uuid.New()
	userID := "time-user"
	ctx := store.WithTenantID(context.Background(), tenantID)
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	if _, err := db.ExecContext(ctx, `INSERT INTO tenants (id, name, slug, status) VALUES (?, ?, ?, ?)`, tenantID.String(), "Test Tenant", "test-tenant", "active"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO agents (id, agent_key, display_name, owner_id, model, tenant_id) VALUES (?, ?, ?, ?, ?, ?)`, agentID.String(), "test-agent", "Test Agent", "owner", "test-model", tenantID.String()); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	items := []struct {
		sourceID  string
		summary   string
		createdAt time.Time
		expiresAt *time.Time
	}{
		{sourceID: "old", summary: "Project Alpha old planning note", createdAt: base.Add(-48 * time.Hour)},
		{sourceID: "recent", summary: "Project Alpha recent planning note", createdAt: base.Add(-2 * time.Hour)},
		{sourceID: "expired", summary: "Project Alpha expired planning note", createdAt: base.Add(-time.Hour), expiresAt: sqliteEpisodicTimePtr(base.Add(-30 * time.Minute))},
	}
	for _, item := range items {
		ep := &store.EpisodicSummary{
			TenantID:   tenantID,
			AgentID:    agentID,
			UserID:     userID,
			SessionKey: "time-sess",
			Summary:    item.summary,
			KeyTopics:  []string{"project-alpha", "planning"},
			SourceType: "session",
			SourceID:   "time-filter-" + item.sourceID,
			ExpiresAt:  item.expiresAt,
		}
		if err := s.Create(ctx, ep); err != nil {
			t.Fatalf("Create %s: %v", item.sourceID, err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE episodic_summaries SET created_at = ? WHERE id = ?`, item.createdAt.UTC().Format(time.RFC3339Nano), ep.ID.String()); err != nil {
			t.Fatalf("set created_at %s: %v", item.sourceID, err)
		}
	}

	createdAfter := base.Add(-24 * time.Hour)
	results, err := s.Search(ctx, "*", agentID.String(), userID, store.EpisodicSearchOptions{
		MaxResults:   10,
		CreatedAfter: &createdAfter,
	})
	if err != nil {
		t.Fatalf("Search recent: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("recent non-expired results = %d, want 1", len(results))
	}
	if results[0].L0Abstract != "Project Alpha recent planning note" {
		t.Fatalf("recent result = %q, want non-expired recent note", results[0].L0Abstract)
	}

	results, err = s.Search(ctx, "planning", agentID.String(), userID, store.EpisodicSearchOptions{
		MaxResults:     10,
		CreatedAfter:   &createdAfter,
		IncludeExpired: true,
	})
	if err != nil {
		t.Fatalf("Search include expired: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("include expired results = %d, want 2", len(results))
	}
}

func sqliteEpisodicTimePtr(t time.Time) *time.Time { return &t }

func seedMatchAnyEpisode(t *testing.T) (context.Context, *SQLiteEpisodicStore, string, string) {
	t.Helper()
	db := openTestDB(t)
	if err := EnsureSchema(db); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	s := NewSQLiteEpisodicStore(db)
	tenantID := uuid.New()
	agentID := uuid.New()
	const userID = "any-user"
	ctx := store.WithTenantID(context.Background(), tenantID)
	if _, err := db.ExecContext(ctx, `INSERT INTO tenants (id, name, slug, status) VALUES (?, ?, ?, ?)`, tenantID.String(), "Test Tenant", "test-tenant", "active"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO agents (id, agent_key, display_name, owner_id, model, tenant_id) VALUES (?, ?, ?, ?, ?, ?)`, agentID.String(), "test-agent", "Test Agent", "owner", "test-model", tenantID.String()); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	if err := s.Create(ctx, &store.EpisodicSummary{
		TenantID:   tenantID,
		AgentID:    agentID,
		UserID:     userID,
		SessionKey: "any-sess",
		Summary:    "User planned a Kubernetes rollback for billing",
		KeyTopics:  []string{"deployment"},
		L0Abstract: "Rollback plan",
		SourceType: "session",
		SourceID:   "any-1",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return ctx, s, agentID.String(), userID
}

func TestSQLiteEpisodicSearch_MatchAnyTerm(t *testing.T) {
	ctx, s, agentID, userID := seedMatchAnyEpisode(t)

	results, err := s.Search(ctx, "kubernetes banana", agentID, userID, store.EpisodicSearchOptions{MaxResults: 5, MatchAnyTerm: true})
	if err != nil {
		t.Fatalf("Search match any: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("match any results = %d, want 1", len(results))
	}
	if results[0].Score != 0.5 {
		t.Errorf("score = %v, want 0.5 (1 of 2 terms)", results[0].Score)
	}

	results, err = s.Search(ctx, "KUBERNETES deployment", agentID, userID, store.EpisodicSearchOptions{MaxResults: 5, MatchAnyTerm: true})
	if err != nil {
		t.Fatalf("Search all terms: %v", err)
	}
	if len(results) != 1 || results[0].Score != 1 {
		t.Errorf("results = %+v, want score 1 when every term matches across summary and topics", results)
	}

	results, err = s.Search(ctx, "kubernetes banana", agentID, userID, store.EpisodicSearchOptions{MaxResults: 5})
	if err != nil {
		t.Fatalf("Search default: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("default results = %d, want 0 because the whole phrase must match", len(results))
	}
}

func TestSQLiteEpisodicSearch_TextQueryReplacesQuery(t *testing.T) {
	ctx, s, agentID, userID := seedMatchAnyEpisode(t)
	const enriched = "Context: earlier chat\nQuery: rollback"

	results, err := s.Search(ctx, enriched, agentID, userID, store.EpisodicSearchOptions{MaxResults: 5, TextQuery: "rollback"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
}

func TestSQLiteEpisodicSearch_MatchAnyTermOnlyStopwords(t *testing.T) {
	ctx, s, agentID, userID := seedMatchAnyEpisode(t)

	results, err := s.Search(ctx, "the and of a", agentID, userID, store.EpisodicSearchOptions{MaxResults: 5, MatchAnyTerm: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("results = %d, want 0", len(results))
	}
}

func TestEpisodicSearchTerms(t *testing.T) {
	got := episodicSearchTerms("The Kế hoạch, kế hoạch rollback! x 2 k8s")
	want := []string{"kế", "hoạch", "rollback", "k8s"}
	if len(got) != len(want) {
		t.Fatalf("terms = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("terms[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
