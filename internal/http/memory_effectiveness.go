package http

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// Khóa system_configs của ngưỡng KPI và công tắc memory, cùng mặc định.
const (
	cfgAutoInjectEnabled = "memory.auto_inject.enabled"
	cfgEvalEnabled       = "memory.eval.enabled"
	cfgKPIMinSamples     = "memory.kpi.min_samples"
	cfgKPIKeepRelevance  = "memory.kpi.keep_relevance"
	cfgKPIKeepUsed       = "memory.kpi.keep_used"
	cfgKPIOffRelevance   = "memory.kpi.off_relevance"
)

var defaultEffectivenessThresholds = effectivenessThresholds{
	MinSamples: 30, KeepRelevance: 0.7, KeepUsed: 0.3, OffRelevance: 0.4,
}

// MemoryEffectivenessHandler phục vụ GET /v1/memory/effectiveness từ bảng rollup memory_kpi_daily.
type MemoryEffectivenessHandler struct {
	kpi     store.MemoryKPIStore
	configs store.SystemConfigStore
	now     func() time.Time
}

func NewMemoryEffectivenessHandler(kpi store.MemoryKPIStore, configs store.SystemConfigStore) *MemoryEffectivenessHandler {
	return &MemoryEffectivenessHandler{kpi: kpi, configs: configs, now: time.Now}
}

func (h *MemoryEffectivenessHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/memory/effectiveness", requireAuth("", h.handleGet))
}

func (h *MemoryEffectivenessHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	days := 7
	if raw := q.Get("days"); raw != "" {
		switch raw {
		case "7", "30", "90":
			days, _ = strconv.Atoi(raw)
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "days must be 7, 30 or 90"})
			return
		}
	}
	var agentID *uuid.UUID
	if raw := q.Get("agent_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid agent_id"})
			return
		}
		agentID = &id
	}

	// Khoảng ngày UTC gồm cả hôm nay.
	to := store.MemoryKPIDay(h.now())
	from := to.AddDate(0, 0, -(days - 1))
	ctx := r.Context()
	rows, err := h.kpi.QueryDaily(ctx, agentID, from, to)
	if err != nil {
		if errors.Is(err, store.ErrMemoryKPITenantRequired) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "tenant required"})
			return
		}
		slog.Warn("memory.effectiveness.query_failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load memory effectiveness"})
		return
	}

	window := effectivenessWindow{From: store.MemoryKPIDayKey(from), To: store.MemoryKPIDayKey(to), Days: days}
	writeJSON(w, http.StatusOK, buildEffectiveness(rows, h.readThresholds(ctx), h.readSwitches(ctx), window))
}

// readConfig trả giá trị thô; thiếu khóa hoặc lỗi đọc thì ok=false để dùng mặc định.
func (h *MemoryEffectivenessHandler) readConfig(ctx context.Context, key string) (string, bool) {
	if h.configs == nil {
		return "", false
	}
	raw, err := h.configs.Get(ctx, key)
	if err != nil {
		if !errors.Is(err, store.ErrSystemConfigNotFound) {
			slog.Warn("memory.effectiveness.config_read_failed", "key", key, "error", err)
		}
		return "", false
	}
	return strings.TrimSpace(raw), true
}

func (h *MemoryEffectivenessHandler) readSwitches(ctx context.Context) effectivenessSwitches {
	flag := func(key string) bool {
		raw, ok := h.readConfig(ctx, key)
		if !ok {
			return true
		}
		v, err := strconv.ParseBool(raw)
		if err != nil {
			slog.Warn("memory.effectiveness.invalid_config", "key", key)
			return true
		}
		return v
	}
	return effectivenessSwitches{AutoInjectEnabled: flag(cfgAutoInjectEnabled), EvalEnabled: flag(cfgEvalEnabled)}
}

func (h *MemoryEffectivenessHandler) readThresholds(ctx context.Context) effectivenessThresholds {
	th := defaultEffectivenessThresholds
	ratioOf := func(key string, def float64) float64 {
		raw, ok := h.readConfig(ctx, key)
		if !ok {
			return def
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
			slog.Warn("memory.effectiveness.invalid_config", "key", key)
			return def
		}
		return v
	}
	if raw, ok := h.readConfig(ctx, cfgKPIMinSamples); ok {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1 {
			th.MinSamples = n
		} else {
			slog.Warn("memory.effectiveness.invalid_config", "key", cfgKPIMinSamples)
		}
	}
	th.KeepRelevance = ratioOf(cfgKPIKeepRelevance, th.KeepRelevance)
	th.KeepUsed = ratioOf(cfgKPIKeepUsed, th.KeepUsed)
	th.OffRelevance = ratioOf(cfgKPIOffRelevance, th.OffRelevance)
	return th
}
