package memoryeval

import (
	"context"
	"log/slog"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/safego"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	rollupInterval    = time.Hour
	rollupStepTimeout = 5 * time.Minute
	// rollupBackfillDays khớp hạn giữ trace: ngày cũ hơn không còn nguồn để tính lại.
	rollupBackfillDays  = 7
	metricRetentionDays = 30
	evalRetentionDays   = 180
)

// RollupWorker duy trì memory_kpi_daily. Chỉ tính lại hôm nay và hôm qua, cùng các ngày đang
// thiếu lúc khởi động. Ngày cũ hơn không bao giờ bị ghi đè, nên số liệu còn nguyên sau khi
// trace nguồn bị xóa ở ngày thứ 7.
type RollupWorker struct {
	lifecycle
	store        store.MemoryKPIStore
	now          func() time.Time
	interval     time.Duration
	lastPruneDay string // chỉ goroutine worker truy cập
}

// NewRollupWorker tạo worker, chưa chạy cho tới khi gọi Start.
func NewRollupWorker(kpi store.MemoryKPIStore) *RollupWorker {
	return &RollupWorker{
		lifecycle: newLifecycle(),
		store:     kpi,
		now:       time.Now,
		interval:  rollupInterval,
	}
}

// Start chạy worker. Gọi nhiều lần chỉ có tác dụng một lần.
func (w *RollupWorker) Start() {
	w.start(func(ctx context.Context) {
		w.backfillMissingDays(ctx)
		w.tick(ctx)
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				w.tick(ctx)
			}
		}
	})
}

// backfillMissingDays điền các ngày còn thiếu trong tuần gần nhất, không kể hôm nay vì tick
// ngay sau đó sẽ tính. Chỉ phần cộng dồn: giá trị cuối ngày của quá khứ không tính lại được
// từ trạng thái hiện tại.
func (w *RollupWorker) backfillMissingDays(ctx context.Context) {
	defer safego.Recover(nil, "worker", "memory_kpi_backfill")
	today := store.MemoryKPIDay(w.now())
	days := make([]time.Time, 0, rollupBackfillDays)
	for i := 1; i <= rollupBackfillDays; i++ {
		days = append(days, today.AddDate(0, 0, -i))
	}
	stepCtx, cancel := context.WithTimeout(ctx, rollupStepTimeout)
	defer cancel()
	missing, err := w.store.MissingRollupDays(stepCtx, days)
	if err != nil {
		slog.Warn("memory.kpi.missing_days_failed", "error", err)
		return
	}
	for _, day := range missing {
		w.refresh(ctx, day, false)
	}
}

// tick tính lại hôm qua (số cuối ngày hôm qua đã chốt ở tick trước) và hôm nay, rồi dọn dữ
// liệu thô một lần mỗi ngày UTC.
func (w *RollupWorker) tick(ctx context.Context) {
	defer safego.Recover(nil, "worker", "memory_kpi_rollup")
	now := w.now().UTC()
	today := store.MemoryKPIDay(now)
	w.refresh(ctx, today.AddDate(0, 0, -1), false)
	w.refresh(ctx, today, true)

	if key := store.MemoryKPIDayKey(today); key != w.lastPruneDay {
		w.lastPruneDay = key
		w.prune(ctx, now)
	}
}

func (w *RollupWorker) refresh(ctx context.Context, day time.Time, includeGauges bool) {
	stepCtx, cancel := context.WithTimeout(ctx, rollupStepTimeout)
	defer cancel()
	if err := w.store.RefreshDailyRollup(stepCtx, day, includeGauges); err != nil {
		slog.Warn("memory.kpi.refresh_failed", "day", store.MemoryKPIDayKey(day), "error", err)
	}
}

func (w *RollupWorker) prune(ctx context.Context, now time.Time) {
	stepCtx, cancel := context.WithTimeout(ctx, rollupStepTimeout)
	defer cancel()
	deleted, err := w.store.PruneRawMetrics(stepCtx, store.MemoryKPIPruneCutoff{
		Metrics:     now.AddDate(0, 0, -metricRetentionDays),
		EvalResults: now.AddDate(0, 0, -evalRetentionDays),
	})
	if err != nil {
		slog.Warn("memory.kpi.prune_failed", "error", err)
		return
	}
	if deleted > 0 {
		slog.Info("memory.kpi.pruned", "rows", deleted)
	}
}
