package cmd

import (
	"github.com/nextlevelbuilder/goclaw/internal/memoryeval"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	usagecaps "github.com/nextlevelbuilder/goclaw/internal/usage/caps"
)

// startMemoryKPIWorkers chạy bộ chấm memory và worker rollup, trên cả PG và SQLite. Trả
// callback nhận ứng viên (nil khi không có store) và hàm dừng cả hai worker. usageCapSvc nil
// trên Lite vẫn dùng được vì Service.Chat an toàn với nil receiver.
func startMemoryKPIWorkers(stores *store.Stores, registry *providers.Registry, usageCapSvc *usagecaps.Service) (func(memoryeval.Candidate), func()) {
	if stores.MemoryKPI == nil {
		return nil, func() {}
	}
	evaluator := memoryeval.NewEvaluator(memoryeval.EvaluatorDeps{
		Store:         stores.MemoryKPI,
		SystemConfigs: stores.SystemConfigs,
		Registry:      registry,
		UsageCaps:     usageCapSvc,
	})
	rollup := memoryeval.NewRollupWorker(stores.MemoryKPI)
	evaluator.Start()
	rollup.Start()
	return func(c memoryeval.Candidate) { evaluator.Submit(c) }, func() {
		rollup.Stop()
		evaluator.Stop()
	}
}
