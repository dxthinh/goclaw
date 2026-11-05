package tracing

import (
	"context"
	"errors"
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

type failOnceAggregateStore struct {
	aggregateRecordingStore
	failed bool
}

func (s *failOnceAggregateStore) BatchUpdateTraceAggregates(ctx context.Context, traceID uuid.UUID) error {
	if !s.failed {
		s.failed = true
		return errors.New("db tạm thời lỗi")
	}
	return s.aggregateRecordingStore.BatchUpdateTraceAggregates(ctx, traceID)
}

func TestFlushRetriesAggregatesAfterFailure(t *testing.T) {
	ts := &failOnceAggregateStore{}
	collector := newTestCollector(ts)
	traceID := uuid.New()
	collector.FinishTrace(context.Background(), traceID, "completed", "", "")
	collector.flush()
	if len(ts.aggregated) != 0 {
		t.Fatalf("lần đầu phải lỗi, đã ghi %v", ts.aggregated)
	}
	collector.flush()
	if len(ts.aggregated) != 1 || ts.aggregated[0] != traceID {
		t.Fatalf("trace phải được tính lại ở lần flush sau: %v", ts.aggregated)
	}
}
