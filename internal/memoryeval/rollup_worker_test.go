package memoryeval

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

func newTestRollupWorker(now *time.Time) (*RollupWorker, *fakeKPIStore) {
	fake := &fakeKPIStore{}
	w := NewRollupWorker(fake)
	w.now = func() time.Time { return *now }
	return w, fake
}

func TestRollupWorker_BackfillOnlyMissingDaysWithoutGauges(t *testing.T) {
	now := time.Date(2026, 11, 6, 8, 30, 0, 0, time.UTC)
	today := store.MemoryKPIDay(now)
	w, fake := newTestRollupWorker(&now)
	missing := today.AddDate(0, 0, -3)
	fake.missing = []time.Time{missing}

	w.backfillMissingDays(context.Background())

	if len(fake.asked) != rollupBackfillDays {
		t.Fatalf("hỏi %d ngày, muốn %d", len(fake.asked), rollupBackfillDays)
	}
	for _, day := range fake.asked {
		if !day.Before(today) || day.Before(today.AddDate(0, 0, -rollupBackfillDays)) {
			t.Fatalf("ngày %v ngoài khoảng 7 ngày gần nhất", day)
		}
	}
	if len(fake.refreshes) != 1 || !fake.refreshes[0].day.Equal(missing) || fake.refreshes[0].gauges {
		t.Fatalf("chỉ được điền ngày thiếu, không gauge: %+v", fake.refreshes)
	}
}

func TestRollupWorker_TickRefreshesOnlyYesterdayAndToday(t *testing.T) {
	now := time.Date(2026, 11, 6, 8, 30, 0, 0, time.UTC)
	today := store.MemoryKPIDay(now)
	w, fake := newTestRollupWorker(&now)

	w.tick(context.Background())

	if len(fake.refreshes) != 2 {
		t.Fatalf("refreshes = %+v, muốn 2", fake.refreshes)
	}
	yesterday, current := fake.refreshes[0], fake.refreshes[1]
	if !yesterday.day.Equal(today.AddDate(0, 0, -1)) || yesterday.gauges {
		t.Fatalf("hôm qua phải không có gauge: %+v", yesterday)
	}
	if !current.day.Equal(today) || !current.gauges {
		t.Fatalf("hôm nay phải có gauge: %+v", current)
	}
}

// Ngày UTC khác ngày địa phương: mốc phải tính theo UTC.
func TestRollupWorker_UsesUTCDay(t *testing.T) {
	saigon := time.FixedZone("ICT", 7*3600)
	now := time.Date(2026, 11, 6, 3, 0, 0, 0, saigon) // 20:00 UTC ngày 5
	w, fake := newTestRollupWorker(&now)
	w.tick(context.Background())
	if want := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC); !fake.refreshes[1].day.Equal(want) {
		t.Fatalf("hôm nay = %v, muốn %v", fake.refreshes[1].day, want)
	}
}

func TestRollupWorker_PrunesOncePerDayWithRetention(t *testing.T) {
	now := time.Date(2026, 11, 6, 8, 30, 0, 0, time.UTC)
	w, fake := newTestRollupWorker(&now)

	w.tick(context.Background())
	now = now.Add(time.Hour)
	w.tick(context.Background())
	if len(fake.prunes) != 1 {
		t.Fatalf("prunes = %d trong cùng ngày, muốn 1", len(fake.prunes))
	}
	first := fake.prunes[0]
	if want := time.Date(2026, 10, 7, 8, 30, 0, 0, time.UTC); !first.Metrics.Equal(want) {
		t.Fatalf("mốc metric = %v, muốn %v", first.Metrics, want)
	}
	if want := time.Date(2026, 5, 10, 8, 30, 0, 0, time.UTC); !first.EvalResults.Equal(want) {
		t.Fatalf("mốc eval = %v, muốn %v", first.EvalResults, want)
	}

	now = now.Add(24 * time.Hour)
	w.tick(context.Background())
	if len(fake.prunes) != 2 {
		t.Fatalf("prunes = %d sau khi sang ngày, muốn 2", len(fake.prunes))
	}
}

func TestRollupWorker_ErrorsAreOnlyLogged(t *testing.T) {
	now := time.Date(2026, 11, 6, 8, 30, 0, 0, time.UTC)
	w, fake := newTestRollupWorker(&now)
	fake.refreshErr = errors.New("db down")
	w.tick(context.Background())
	if len(fake.refreshes) != 2 {
		t.Fatalf("lỗi ở hôm qua không được chặn hôm nay: %+v", fake.refreshes)
	}
}

func TestRollupWorker_StartStop(t *testing.T) {
	now := time.Date(2026, 11, 6, 8, 30, 0, 0, time.UTC)
	w, fake := newTestRollupWorker(&now)
	w.interval = time.Hour
	w.Start()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		n := len(fake.prunes)
		fake.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	stopped := make(chan struct{})
	go func() {
		w.Stop()
		w.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop không kết thúc")
	}
	if len(fake.prunes) != 1 || len(fake.refreshes) != 2 {
		t.Fatalf("khởi động phải chạy một lần: prunes = %d, refreshes = %d", len(fake.prunes), len(fake.refreshes))
	}
}

// Stop trước Start không được treo.
func TestRollupWorker_StopWithoutStart(t *testing.T) {
	now := time.Now()
	w, _ := newTestRollupWorker(&now)
	w.Stop()
}
