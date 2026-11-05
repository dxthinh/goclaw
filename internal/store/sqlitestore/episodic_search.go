//go:build sqlite || sqliteonly

package sqlitestore

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// episodicSearchStopwords là các từ phổ biến bị bỏ khi MatchAnyTerm. Gần tương đương
// bộ lọc của cấu hình 'english' bên PG; chỉ có tiếng Anh.
var episodicSearchStopwords = func() map[string]struct{} {
	words := strings.Fields(`a about all also am an and any are as at be been but by can could did do does
		for from had has have he her him his how i if in into is it its me more my no not of on or our she
		so than that the their them then there these they this to too very was we were what when where which
		who why will with would you your`)
	set := make(map[string]struct{}, len(words))
	for _, w := range words {
		set[w] = struct{}{}
	}
	return set
}()

// episodicSearchTerms tách truy vấn thành các từ khóa khác nhau đã viết thường, bỏ
// stopword và từ một ký tự.
func episodicSearchTerms(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r)
	})
	terms := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		if _, stop := episodicSearchStopwords[f]; stop || len([]rune(f)) < 2 {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		terms = append(terms, f)
	}
	return terms
}

// Search performs LIKE-based text search over episodic summaries.
// Vector search is not available in the SQLite edition.
// Scoring: 1.0 base, +0.2 if l0_abstract matches, +0.1 if key_topics matches.
//
// Với MatchAnyTerm, điểm là tỉ lệ từ khóa khớp (0..1) thay vì cộng thưởng, để ngưỡng
// của auto-inject vẫn có nghĩa khi không có cosine: ngưỡng 0,8 nghĩa là phải khớp ít
// nhất 80% từ. Giới hạn so với PG: LIKE khớp chuỗi con, không tách gốc từ, chỉ không
// phân biệt hoa thường với ASCII, và chỉ chấm trên các dòng mới nhất đã lọc bằng LIMIT.
func (s *SQLiteEpisodicStore) Search(ctx context.Context, query string, agentID, userID string, opts store.EpisodicSearchOptions) ([]store.EpisodicSearchResult, error) {
	listOnly := isEpisodicListOnly(query)
	// TextQuery thay query cho tìm văn bản; SQLite không có embedding nên query chỉ
	// còn dùng để nhận biết chế độ liệt kê.
	if tq := strings.TrimSpace(opts.TextQuery); tq != "" {
		query = tq
	}
	// F10: cap query to prevent degenerate LIKE patterns
	if len(query) > 500 {
		query = query[:500]
	}

	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}

	tenantID := tenantIDForInsert(ctx)
	query = strings.TrimSpace(query)

	var terms []string
	matchAny := opts.MatchAnyTerm && !listOnly
	if matchAny {
		terms = episodicSearchTerms(query)
		if len(terms) == 0 {
			return nil, nil // toàn stopword: không có gì để khớp
		}
	}

	q := `
		SELECT id, COALESCE(NULLIF(l0_abstract, ''), substr(summary, 1, 500)) AS l0_abstract, key_topics, created_at, expires_at, session_key, summary
		FROM episodic_summaries
		WHERE agent_id = ? AND tenant_id = ?`
	args := []any{agentID, tenantID.String()}
	switch {
	case matchAny:
		clauses := make([]string, len(terms))
		for i, term := range terms {
			pattern := "%" + escapeLike(term) + "%"
			clauses[i] = "summary LIKE ? ESCAPE '\\' OR key_topics LIKE ? ESCAPE '\\'"
			args = append(args, pattern, pattern)
		}
		q += " AND (" + strings.Join(clauses, " OR ") + ")"
	case !listOnly:
		pattern := "%" + escapeLike(query) + "%"
		q += " AND (summary LIKE ? ESCAPE '\\' OR key_topics LIKE ? ESCAPE '\\')"
		args = append(args, pattern, pattern)
	}
	if store.IsSharedMemory(ctx) {
		// Shared memory searches all user scopes for the agent.
	} else if userID != "" {
		q += " AND (user_id = ? OR user_id = '')"
		args = append(args, userID)
	} else {
		q += " AND user_id = ''"
	}
	if opts.CreatedAfter != nil {
		q += " AND created_at >= ?"
		args = append(args, opts.CreatedAfter.UTC().Format(time.RFC3339Nano))
	}
	if opts.CreatedBefore != nil {
		q += " AND created_at < ?"
		args = append(args, opts.CreatedBefore.UTC().Format(time.RFC3339Nano))
	}
	if !opts.IncludeExpired {
		q += " AND (expires_at IS NULL OR expires_at > ?)"
		args = append(args, time.Now().UTC().Format(time.RFC3339Nano))
	}
	q += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, maxResults*3)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type rawRow struct {
		id         string
		l0Abstract string
		keyTopics  string
		createdAt  sqliteTime
		expiresAt  nullSqliteTime
		sessionKey string
		summary    string
	}

	var raws []rawRow
	for rows.Next() {
		var r rawRow
		if err := rows.Scan(&r.id, &r.l0Abstract, &r.keyTopics, &r.createdAt, &r.expiresAt, &r.sessionKey, &r.summary); err != nil {
			continue
		}
		raws = append(raws, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Post-query scoring
	lowerQuery := strings.ToLower(query)
	type scored struct {
		raw   rawRow
		score float64
	}
	scoredRows := make([]scored, 0, len(raws))
	for _, r := range raws {
		sc := 1.0
		switch {
		case matchAny:
			haystack := strings.ToLower(r.summary + " " + r.keyTopics)
			matched := 0
			for _, term := range terms {
				if strings.Contains(haystack, term) {
					matched++
				}
			}
			sc = float64(matched) / float64(len(terms))
		case !listOnly:
			if strings.Contains(strings.ToLower(r.l0Abstract), lowerQuery) {
				sc += 0.2
			}
			if strings.Contains(strings.ToLower(r.keyTopics), lowerQuery) {
				sc += 0.1
			}
		}
		scoredRows = append(scoredRows, scored{raw: r, score: sc})
	}

	// Sort by score DESC, then created_at DESC
	sort.SliceStable(scoredRows, func(i, j int) bool {
		if scoredRows[i].score != scoredRows[j].score {
			return scoredRows[i].score > scoredRows[j].score
		}
		return scoredRows[i].raw.createdAt.Time.After(scoredRows[j].raw.createdAt.Time)
	})

	var results []store.EpisodicSearchResult
	for _, sr := range scoredRows {
		if opts.MinScore > 0 && sr.score < opts.MinScore {
			continue
		}
		var expiresAt *time.Time
		if sr.raw.expiresAt.Valid {
			t := sr.raw.expiresAt.Time
			expiresAt = &t
		}
		results = append(results, store.EpisodicSearchResult{
			EpisodicID: sr.raw.id,
			L0Abstract: sr.raw.l0Abstract,
			KeyTopics:  searchKeyTopics(sr.raw.keyTopics),
			Score:      sr.score,
			CreatedAt:  sr.raw.createdAt.Time,
			ExpiresAt:  expiresAt,
			SessionKey: sr.raw.sessionKey,
		})
		if len(results) >= maxResults {
			break
		}
	}
	return results, nil
}

// isEpisodicListOnly cho biết truy vấn chỉ yêu cầu liệt kê theo thời gian, không tìm kiếm.
func isEpisodicListOnly(query string) bool {
	q := strings.TrimSpace(query)
	return q == "" || q == "*"
}

func searchKeyTopics(raw string) []string {
	var topics []string
	scanJSONStringArray([]byte(raw), &topics)
	return topics
}

// Ensure SQLiteEpisodicStore implements store.EpisodicStore.
var _ store.EpisodicStore = (*SQLiteEpisodicStore)(nil)
