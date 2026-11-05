package memoryeval

import (
	"context"
	"sync"
	"sync/atomic"
)

// lifecycle là phần khởi động và dừng dùng chung của các worker nền.
type lifecycle struct {
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	started   atomic.Bool
	startOnce sync.Once
}

func newLifecycle() lifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return lifecycle{ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

func (l *lifecycle) start(run func(ctx context.Context)) {
	l.startOnce.Do(func() {
		l.started.Store(true)
		go func() {
			defer close(l.done)
			run(l.ctx)
		}()
	})
}

// Stop hủy việc đang chạy và chờ goroutine thoát. Gọi nhiều lần vẫn an toàn.
func (l *lifecycle) Stop() {
	l.cancel()
	if l.started.Load() {
		<-l.done
	}
}
