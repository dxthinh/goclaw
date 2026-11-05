package tracing

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

type aggregateRecordingStore struct {
	mockTracingStore
	aggregated []uuid.UUID
}

func (s *aggregateRecordingStore) BatchUpdateTraceAggregates(_ context.Context, traceID uuid.UUID) error {
	s.aggregated = append(s.aggregated, traceID)
	return nil
}

func TestFinishTraceFlushesAggregatesWithoutNewSpans(t *testing.T) {
	ts := &aggregateRecordingStore{}
	collector := newTestCollector(ts)
	traceID := uuid.New()
	collector.FinishTrace(context.Background(), traceID, "completed", "", "")
	collector.flush()
	if len(ts.aggregated) != 1 || ts.aggregated[0] != traceID {
		t.Fatalf("trace cần tổng hợp = %v, muốn [%s]", ts.aggregated, traceID)
	}
}
