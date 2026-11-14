package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// AdaptationGuardrails controls auto-adaptation safety limits.
// Stored in agent other_config JSONB under "evolution_guardrails".
type AdaptationGuardrails struct {
	MaxDeltaPerCycle float64  `json:"max_delta_per_cycle"`     // max parameter change per cycle (default 0.1)
	MinDataPoints    int      `json:"min_data_points"`         // min metrics before applying (default 100)
	RollbackOnDrop   float64  `json:"rollback_on_drop_pct"`    // quality drop % triggering rollback (default 20.0)
	LockedParams     []string `json:"locked_params,omitempty"` // params that cannot be auto-changed
}

// DefaultGuardrails returns conservative defaults.
func DefaultGuardrails() AdaptationGuardrails {
	return AdaptationGuardrails{
		MaxDeltaPerCycle: 0.1,
		MinDataPoints:    100,
		RollbackOnDrop:   20.0,
	}
}

// CheckGuardrails validates a suggestion against guardrail constraints.
// Returns an error describing the violation, or nil if safe to apply.
func CheckGuardrails(g AdaptationGuardrails, sg store.EvolutionSuggestion, dataPoints int) error {
	// Check minimum data points.
	minDP := g.MinDataPoints
	if minDP <= 0 {
		minDP = 100
	}
	if dataPoints < minDP {
		return fmt.Errorf("insufficient data: %d points, need %d", dataPoints, minDP)
	}

	// Check locked parameters.
	if len(g.LockedParams) > 0 {
		var params map[string]any
		if err := json.Unmarshal(sg.Parameters, &params); err == nil {
			for key := range params {
				if slices.Contains(g.LockedParams, key) {
					return fmt.Errorf("parameter %q is locked", key)
				}
			}
		}
	}

	if sg.SuggestionType == store.SuggestThreshold {
		return fmt.Errorf("retrieval threshold adaptation is unsupported")
	}

	return nil
}

// ApplySuggestion rejects unsupported adaptation. No per-agent retrieval threshold
// is consumed by retrieval; result presence is not evidence of answer quality.
func ApplySuggestion(ctx context.Context, agentStore store.AgentStore, sugStore store.EvolutionSuggestionStore, sg store.EvolutionSuggestion, guardrails AdaptationGuardrails) error {
	return fmt.Errorf("suggestion type %q does not support auto-apply", sg.SuggestionType)
}

// RollbackSuggestion reverts an applied suggestion by restoring baseline values.
func RollbackSuggestion(ctx context.Context, agentStore store.AgentStore, sugStore store.EvolutionSuggestionStore, sg store.EvolutionSuggestion) error {
	// Extract baseline from suggestion params.
	var sgParams map[string]any
	if err := json.Unmarshal(sg.Parameters, &sgParams); err != nil {
		return fmt.Errorf("parse suggestion params: %w", err)
	}
	baseline, _ := sgParams["_baseline"].(map[string]any)
	if baseline == nil {
		return fmt.Errorf("no baseline data for rollback")
	}

	// Load current config and restore baseline values.
	agent, err := agentStore.GetByID(ctx, sg.AgentID)
	if err != nil {
		return fmt.Errorf("load agent: %w", err)
	}
	var otherConfig map[string]any
	if len(agent.OtherConfig) > 0 {
		_ = json.Unmarshal(agent.OtherConfig, &otherConfig)
	}
	if otherConfig == nil {
		otherConfig = make(map[string]any)
	}

	// Restore each baseline parameter.
	maps.Copy(otherConfig, baseline)

	configJSON, _ := json.Marshal(otherConfig)
	if err := agentStore.Update(ctx, sg.AgentID, map[string]any{
		"other_config": json.RawMessage(configJSON),
	}); err != nil {
		return fmt.Errorf("rollback agent config: %w", err)
	}

	if err := sugStore.UpdateSuggestionStatus(ctx, sg.ID, "rolled_back", "auto-adapt"); err != nil {
		return fmt.Errorf("update suggestion status: %w", err)
	}

	slog.Info("evolution.auto_adapt.rolled_back", "agent", sg.AgentID, "type", sg.SuggestionType)
	return nil
}

// EvaluateApplied is retained for callers; automatic quality-based rollback is
// disabled until answer-quality evidence and an effective adaptation route exist.
func EvaluateApplied(ctx context.Context, agentID uuid.UUID, guardrails AdaptationGuardrails,
	metricsStore store.EvolutionMetricsStore, sugStore store.EvolutionSuggestionStore,
	agentStore store.AgentStore) error {

	// Retrieval result presence cannot measure answer quality. Historical threshold
	// suggestions must not trigger automatic config writes or rollback decisions.
	return nil
}
