package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
	"github.com/nextlevelbuilder/goclaw/internal/tracing"
)

const (
	deliveryRecorderBuffer  = 256
	deliveryRecordTimeout   = 2 * time.Second
	deliveryErrorPreviewMax = 500
)

// deliverySpanEmitter is the part of tracing.Collector the recorder needs.
type deliverySpanEmitter interface {
	EmitSpan(span store.SpanData)
}

// deliverySessionWriter is the part of the session store the recorder needs.
type deliverySessionWriter interface {
	AddMessage(ctx context.Context, key string, msg providers.Message)
	Save(ctx context.Context, key string) error
}

// deliveryRecorder persists attachment delivery outcomes: a timeline item and a
// trace span for each file, plus a note in the session when a file did not arrive,
// so the agent knows on its next turn. One worker keeps a run's items in delivery
// order and keeps database writes off the outbound shards.
type deliveryRecorder struct {
	timeline store.RunTimelineStore
	spans    deliverySpanEmitter
	sessions deliverySessionWriter
	queue    chan channels.DeliveryOutcome
}

func newDeliveryRecorder(timeline store.RunTimelineStore, collector *tracing.Collector, sessions store.SessionStore) *deliveryRecorder {
	r := &deliveryRecorder{timeline: timeline, queue: make(chan channels.DeliveryOutcome, deliveryRecorderBuffer)}
	if collector != nil {
		r.spans = collector
	}
	if sessions != nil {
		r.sessions = sessions
	}
	go func() {
		for outcome := range r.queue {
			r.record(outcome)
		}
	}()
	return r
}

// RecordDelivery implements channels.DeliveryRecorder. It never blocks the shard:
// when the queue is full the entry is dropped, and the log line remains.
func (r *deliveryRecorder) RecordDelivery(_ context.Context, outcome channels.DeliveryOutcome) {
	select {
	case r.queue <- outcome:
	default:
		slog.Warn("delivery.record_dropped", "channel", outcome.Msg.Channel, "path", outcome.Media.URL)
	}
}

func (r *deliveryRecorder) record(outcome channels.DeliveryOutcome) {
	msg := outcome.Msg
	runID := msg.Metadata[bus.MetaDeliveryRunID]
	sessionKey := msg.Metadata[bus.MetaDeliverySessionKey]
	if runID == "" || sessionKey == "" || msg.TenantID == uuid.Nil {
		slog.Debug("delivery.record_skipped", "channel", msg.Channel, "path", outcome.Media.URL)
		return
	}

	ctx, cancel := context.WithTimeout(store.WithTenantID(context.Background(), msg.TenantID), deliveryRecordTimeout)
	defer cancel()

	fileName := filepath.Base(outcome.Media.URL)
	errText := ""
	if outcome.Err != nil {
		errText = tracing.TruncateMid(tools.ScrubCredentials(outcome.Err.Error()), deliveryErrorPreviewMax)
	}
	metadata := deliveryMetadata(outcome, errText)

	var traceID *uuid.UUID
	if parsed, err := uuid.Parse(msg.Metadata[bus.MetaDeliveryTraceID]); err == nil {
		traceID = &parsed
	}
	var agentID *uuid.UUID
	if msg.AgentID != uuid.Nil {
		agentID = &msg.AgentID
	}

	status := store.RunTimelineStatusCompleted
	if outcome.Err != nil {
		status = store.RunTimelineStatusFailed
	}
	if r.timeline != nil {
		item := &store.RunTimelineItem{
			RunID:      runID,
			SessionKey: sessionKey,
			AgentID:    agentID,
			UserID:     msg.Metadata[bus.MetaDeliveryUserID],
			Channel:    msg.Channel,
			ChatID:     msg.ChatID,
			ItemType:   store.RunTimelineItemTypeDelivery,
			Status:     status,
			Title:      fileName,
			Preview:    errText,
			TraceID:    traceID,
			Metadata:   metadata,
		}
		if err := r.timeline.AppendRunTimelineItemNextSeq(ctx, item); err != nil {
			slog.Warn("delivery.timeline_persist_failed", "run_id", runID, "path", outcome.Media.URL, "error", err)
		}
	}

	if r.spans != nil && traceID != nil {
		now := time.Now().UTC()
		spanStatus := store.SpanStatusCompleted
		if outcome.Err != nil {
			spanStatus = store.SpanStatusError
		}
		r.spans.EmitSpan(store.SpanData{
			TraceID:       *traceID,
			AgentID:       agentID,
			SpanType:      store.SpanTypeEvent,
			Name:          "delivery",
			StartTime:     now,
			EndTime:       &now,
			Status:        spanStatus,
			Error:         errText,
			Level:         store.SpanLevelDefault,
			OutputPreview: fileName,
			Metadata:      metadata,
			TenantID:      msg.TenantID,
		})
	}

	if r.sessions != nil && outcome.Err != nil {
		r.sessions.AddMessage(ctx, sessionKey, providers.Message{
			Role:    "user",
			Content: fmt.Sprintf("[System] File %q was not delivered to %s: %s", fileName, msg.Channel, errText),
		})
		if err := r.sessions.Save(ctx, sessionKey); err != nil {
			slog.Warn("delivery.session_note_failed", "session", sessionKey, "error", err)
		}
	}
}

func deliveryMetadata(outcome channels.DeliveryOutcome, errText string) json.RawMessage {
	metadata := map[string]any{
		"path":         outcome.Media.URL,
		"content_type": outcome.Media.ContentType,
		"attempts":     outcome.Attempts,
	}
	if errText != "" {
		metadata["error"] = errText
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil
	}
	return raw
}
