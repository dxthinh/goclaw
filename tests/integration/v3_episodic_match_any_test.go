//go:build integration

package integration

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/store/pg"
)

func seedMatchAnyEpisode(t *testing.T, ctx context.Context, s store.EpisodicStore, tenantID, agentID uuid.UUID, userID string) {
	t.Helper()
	if err := s.Create(ctx, &store.EpisodicSummary{
		TenantID:   tenantID,
		AgentID:    agentID,
		UserID:     userID,
		SessionKey: "match-any-sess",
		Summary:    "User planned a Kubernetes rollback for the billing service",
		KeyTopics:  []string{"rollback"},
		L0Abstract: "Kubernetes rollback plan",
		SourceType: "session",
		SourceID:   "match-any-" + uuid.NewString()[:8],
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

func matchAnyFixture(t *testing.T) (context.Context, *sql.DB, store.EpisodicStore, string, string) {
	t.Helper()
	db := testDB(t)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	s := newEpisodicStore(t)
	userID := "match-any-" + tenantID.String()[:8]
	seedMatchAnyEpisode(t, ctx, s, tenantID, agentID, userID)
	return ctx, db, s, agentID.String(), userID
}

// Một từ khớp phải đủ để có kết quả khi MatchAnyTerm bật; hành vi mặc định vẫn đòi đủ từ.
func TestStoreEpisodic_MatchAnyTerm_SingleTermMatches(t *testing.T) {
	ctx, _, s, agentID, userID := matchAnyFixture(t)
	const query = "kubernetes banana spaceship"

	results, err := s.Search(ctx, query, agentID, userID, store.EpisodicSearchOptions{MaxResults: 5, MatchAnyTerm: true})
	if err != nil {
		t.Fatalf("Search match any: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("match any results = %d, want 1", len(results))
	}
	if results[0].Score <= 0 {
		t.Errorf("match any score = %v, want > 0 from ts_rank", results[0].Score)
	}

	results, err = s.Search(ctx, query, agentID, userID, store.EpisodicSearchOptions{MaxResults: 5})
	if err != nil {
		t.Fatalf("Search match all: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("match all results = %d, want 0 because not every term matches", len(results))
	}
}

// TextQuery thay query cho FTS: nhãn và ngữ cảnh trong query không còn làm FTS trượt.
func TestStoreEpisodic_TextQueryReplacesQueryForFTS(t *testing.T) {
	ctx, _, s, agentID, userID := matchAnyFixture(t)
	const enriched = "Context: unrelated earlier chat\nQuery: rollback"

	results, err := s.Search(ctx, enriched, agentID, userID, store.EpisodicSearchOptions{MaxResults: 5})
	if err != nil {
		t.Fatalf("Search enriched: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("enriched query results = %d, want 0: labels must break AND matching", len(results))
	}

	results, err = s.Search(ctx, enriched, agentID, userID, store.EpisodicSearchOptions{MaxResults: 5, TextQuery: "rollback"})
	if err != nil {
		t.Fatalf("Search with TextQuery: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("TextQuery results = %d, want 1", len(results))
	}
}

// Truy vấn toàn stopword cho tsquery rỗng và không được gây lỗi SQL.
func TestStoreEpisodic_MatchAnyTerm_OnlyStopwordsReturnsEmpty(t *testing.T) {
	ctx, _, s, agentID, userID := matchAnyFixture(t)

	results, err := s.Search(ctx, "the and of", agentID, userID, store.EpisodicSearchOptions{MaxResults: 5, MatchAnyTerm: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("results = %d, want 0", len(results))
	}
}

// Ký tự đặc biệt của tsquery trong chuỗi người dùng không được thành toán tử hay gây lỗi cú pháp.
func TestStoreEpisodic_MatchAnyTerm_SpecialCharactersAreSafe(t *testing.T) {
	ctx, db, s, agentID, userID := matchAnyFixture(t)

	for _, query := range []string{
		`it's 'quoted' & (kubernetes) | !nothing:* <-> \`,
		`kubernetes'; DROP TABLE episodic_summaries; --`,
		strings.Repeat("kubernetes ", 500),
		"a-b kubernetes c.d e@f.com /usr/bin",
	} {
		results, err := s.Search(ctx, query, agentID, userID, store.EpisodicSearchOptions{MaxResults: 5, MatchAnyTerm: true})
		if err != nil {
			t.Errorf("Search(%.40q): %v", query, err)
			continue
		}
		if len(results) != 1 {
			t.Errorf("Search(%.40q) results = %d, want 1", query, len(results))
		}
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM episodic_summaries WHERE agent_id = $1`, agentID).Scan(&count); err != nil {
		t.Fatalf("table must survive: %v", err)
	}
	if count != 1 {
		t.Errorf("episode count = %d, want 1", count)
	}
}

// Metric mới phải ghi được vào bảng thật cùng session_key, không vướng ràng buộc schema.
func TestStoreEvolutionMetrics_RecordsPromptMetricWithSessionKey(t *testing.T) {
	db := testDB(t)
	tenantID, agentID := seedTenantAgent(t, db)
	ctx := tenantCtx(tenantID)
	ms := pg.NewPGEvolutionMetricsStore(db)

	if err := ms.RecordMetric(ctx, store.EvolutionMetric{
		ID:         uuid.New(),
		TenantID:   tenantID,
		AgentID:    agentID,
		SessionKey: "sess-shape",
		MetricType: store.MetricPrompt,
		MetricKey:  "shape",
		Value:      []byte(`{"system_tokens":1}`),
	}); err != nil {
		t.Fatalf("RecordMetric: %v", err)
	}

	var sessionKey string
	err := db.QueryRowContext(context.Background(),
		`SELECT session_key FROM agent_evolution_metrics WHERE agent_id = $1 AND metric_type = 'prompt' AND metric_key = 'shape'`,
		agentID).Scan(&sessionKey)
	if err != nil {
		t.Fatalf("read metric: %v", err)
	}
	if sessionKey != "sess-shape" {
		t.Errorf("session_key = %q, want sess-shape", sessionKey)
	}
}
