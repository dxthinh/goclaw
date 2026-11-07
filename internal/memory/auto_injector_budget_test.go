package memory

import (
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tokencount"
)

func TestInjectionBudgetIncludesFormatting(t *testing.T) {
	counter := tokencount.NewBudgetCounter()
	count := counter.CountText
	for _, text := range []string{"Remember my favorite tea.", "Người dùng thích trà xanh vào buổi sáng.", "用户喜欢在早上喝绿茶。"} {
		t.Run(text, func(t *testing.T) {
			hits := []store.EpisodicSearchResult{{L0Abstract: text, Score: 0.8}}
			full := *evaluateMatchesWithBudget(hits, 0, 5, 1000, count)
			exact := checkedBudgetCount(t, counter, "\n\n"+full.Section)
			for _, budget := range []int{1, exact - 1, exact, exact + 1} {
				got := *evaluateMatchesWithBudget(hits, 0, 5, budget, count)
				if got.Section != "" && checkedBudgetCount(t, counter, "\n\n"+got.Section) > budget {
					t.Fatalf("formatted section exceeds budget %d: %q", budget, got.Section)
				}
				if budget < exact && (got.Section != "" || got.Injected != 0) {
					t.Fatalf("entry must be skipped whole: %+v", got)
				}
				if budget >= exact && (got.Injected != 1 || got.Section != full.Section) {
					t.Fatalf("entry should fit: %+v", got)
				}
			}
		})
	}
}

func TestInjectionDefaultBudgetAndEntryLimit(t *testing.T) {
	counter := tokencount.NewBudgetCounter()
	count := counter.CountText
	hits := []store.EpisodicSearchResult{
		{L0Abstract: strings.Repeat("large memory entry ", 1000), Score: 0.9},
		{L0Abstract: "", Score: 0.8},
	}
	for range 10 {
		hits = append(hits, store.EpisodicSearchResult{L0Abstract: "A short memory.", Score: 0.7})
	}
	for _, budget := range []int{0, -1, 200} {
		got := *evaluateMatchesWithBudget(hits, 0, 5, budget, count)
		if got.Injected != 5 || got.MatchCount != len(hits) || got.TopScore != 0.9 {
			t.Fatalf("unexpected fitted result: %+v", got)
		}
		if checkedBudgetCount(t, counter, "\n\n"+got.Section) > 200 || strings.Contains(got.Section, "large memory") {
			t.Fatal("default budget not enforced")
		}
	}
	got := *evaluateMatchesWithBudget(hits, 0, 1, 200, count)
	if got.Injected != 1 {
		t.Fatalf("entry limit ignored: %+v", got)
	}
}

func checkedBudgetCount(t *testing.T, counter tokencount.BudgetCounter, text string) int {
	t.Helper()
	count, err := counter.CountText(text)
	if err != nil {
		t.Fatal(err)
	}
	return count
}
