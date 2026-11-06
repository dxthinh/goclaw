package memoryeval

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// failureCount đếm lượt gọi thất bại trong một ngày UTC. Lượt thất bại không để lại dòng nào
// nên nếu không đếm riêng, provider trả phản hồi hỏng sẽ tốn lời gọi vô hạn mà không chạm hạn mức.
type failureCount struct {
	day   string
	count int
}

// enabled đọc công tắc theo tenant. Không đọc được hoặc sai cấu hình thì giữ mặc định bật.
func (e *Evaluator) enabled(ctx context.Context) bool {
	raw, ok := e.readConfig(ctx, configEvalEnabled)
	if !ok {
		return true
	}
	enabled, err := strconv.ParseBool(raw)
	return err != nil || enabled
}

// withinDailyLimit so số lượt đã dùng từ đầu ngày UTC với hạn mức. Không đếm được thì bỏ mẫu,
// vì đây là chốt chặn chi phí nên phải an toàn về phía không gọi LLM.
func (e *Evaluator) withinDailyLimit(ctx context.Context, tenantID uuid.UUID) bool {
	limit := defaultDailyLimit
	if raw, ok := e.readConfig(ctx, configEvalDailyLimit); ok {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
			limit = parsed
		}
	}
	now := e.now().UTC()
	stored, err := e.deps.Store.CountEvalsSince(ctx, store.MemoryKPIDay(now))
	if err != nil {
		slog.Warn("memory.eval.count_failed", "tenant", tenantID, "error", err)
		return false
	}
	failed := 0
	if f := e.failures[tenantID]; f.day == store.MemoryKPIDayKey(now) {
		failed = f.count
	}
	return stored+failed < limit
}

func (e *Evaluator) recordFailure(tenantID uuid.UUID) {
	day := store.MemoryKPIDayKey(e.now())
	f := e.failures[tenantID]
	if f.day != day {
		f = failureCount{day: day}
	}
	f.count++
	e.failures[tenantID] = f
}

func (e *Evaluator) readConfig(ctx context.Context, key string) (string, bool) {
	if e.deps.SystemConfigs == nil {
		return "", false
	}
	raw, err := e.deps.SystemConfigs.Get(ctx, key)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(raw), true
}
