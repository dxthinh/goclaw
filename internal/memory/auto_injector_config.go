package memory

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strconv"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	configAutoInjectEnabled  = "memory.auto_inject.enabled"
	configAutoInjectMinScore = "memory.auto_inject.min_score"

	// defaultAutoInjectMinScore áp cho điểm ts_rank + cosine (trọng số 1 và 1). Cosine
	// trung vị giữa các episodic không liên quan đã là 0,73 nên vector đơn lẻ không phân
	// biệt được; 0,8 đòi hỏi phải có thêm tín hiệu từ khóa.
	defaultAutoInjectMinScore = 0.8
)

// autoInjectEnabled đọc công tắc theo tenant trong ctx. Không đọc được thì giữ mặc định bật.
func (a *pgAutoInjector) autoInjectEnabled(ctx context.Context) bool {
	raw, ok := a.readConfig(ctx, configAutoInjectEnabled)
	if !ok {
		return true
	}
	enabled, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		slog.Warn("memory.auto_inject.invalid_config", "key", configAutoInjectEnabled, "error", err)
		return true
	}
	return enabled
}

// autoInjectMinScore đọc ngưỡng theo tenant trong ctx. Giá trị âm hoặc không phải số
// hữu hạn bị coi là sai cấu hình và dùng mặc định.
func (a *pgAutoInjector) autoInjectMinScore(ctx context.Context) float64 {
	raw, ok := a.readConfig(ctx, configAutoInjectMinScore)
	if !ok {
		return defaultAutoInjectMinScore
	}
	score, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || score < 0 || math.IsNaN(score) || math.IsInf(score, 0) {
		slog.Warn("memory.auto_inject.invalid_config", "key", configAutoInjectMinScore, "value", raw)
		return defaultAutoInjectMinScore
	}
	return score
}

func (a *pgAutoInjector) readConfig(ctx context.Context, key string) (string, bool) {
	if a.configStore == nil {
		return "", false
	}
	raw, err := a.configStore.Get(ctx, key)
	if err != nil {
		if !errors.Is(err, store.ErrSystemConfigNotFound) {
			slog.Warn("memory.auto_inject.config_read_failed", "key", key, "error", err)
		}
		return "", false
	}
	return raw, true
}
