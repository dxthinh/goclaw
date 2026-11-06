package tracing

import (
	"context"
	"errors"
	"testing"
	"time"

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

type fillRecordingStore struct {
	mockTracingStore
	filled chan struct{}
}

func (s *fillRecordingStore) DeleteTracesOlderThan(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (s *fillRecordingStore) FillMissingTraceDurations(context.Context) (int64, error) {
	close(s.filled)
	return 0, nil
}

// Start() không chạy staleRecoveryLoop, nên phần bù duration_ms phải được gọi trực tiếp lúc khởi động.
func TestStartFillsMissingTraceDurations(t *testing.T) {
	ts := &fillRecordingStore{filled: make(chan struct{})}
	collector := NewCollector(ts)
	collector.Start()
	defer collector.Stop()
	select {
	case <-ts.filled:
	case <-time.After(5 * time.Second):
		t.Fatal("Start() không gọi FillMissingTraceDurations")
	}
}
