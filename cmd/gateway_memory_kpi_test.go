package cmd

import (
	"context"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/store"
)

type kpiWiringStore struct {
	store.MemoryKPIStore
	refreshed chan struct{}
}

func (s *kpiWiringStore) MissingRollupDays(context.Context, []time.Time) ([]time.Time, error) {
	return nil, nil
}

func (s *kpiWiringStore) RefreshDailyRollup(context.Context, time.Time, bool) error {
	select {
	case s.refreshed <- struct{}{}:
	default:
	}
	return nil
}

func (s *kpiWiringStore) PruneRawMetrics(context.Context, store.MemoryKPIPruneCutoff) (int64, error) {
	return 0, nil
}

func TestStartMemoryKPIWorkers_NoStoreIsNoOp(t *testing.T) {
	submit, stop := startMemoryKPIWorkers(&store.Stores{}, nil, nil)
	if submit != nil {
		t.Fatal("không có store thì không được nhận ứng viên")
	}
	stop()
}

// Service caps nil giống Lite: worker vẫn chạy và dừng sạch.
func TestStartMemoryKPIWorkers_RunsWithoutUsageCaps(t *testing.T) {
	kpi := &kpiWiringStore{refreshed: make(chan struct{}, 1)}
	submit, stop := startMemoryKPIWorkers(&store.Stores{MemoryKPI: kpi}, nil, nil)
	if submit == nil {
		t.Fatal("thiếu callback nhận ứng viên")
	}
	select {
	case <-kpi.refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("worker rollup không chạy khi khởi động")
	}
	stop()
}
