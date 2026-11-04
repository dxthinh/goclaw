package agent

import (
	"context"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

func retryableFailure() *tools.Result {
	result := tools.ErrorResult("fetch failed: connection reset")
	result.Retryable = true
	return result
}

func noToolBackoff(t *testing.T) {
	t.Helper()
	orig := toolRetryBackoff
	toolRetryBackoff = []time.Duration{0, 0}
	t.Cleanup(func() { toolRetryBackoff = orig })
}

func TestTransientRetrySucceedsOnLastAttempt(t *testing.T) {
	noToolBackoff(t)
	calls := 0
	result, retries := executeWithTransientRetry(context.Background(), "web_fetch", func() *tools.Result {
		calls++
		if calls < 3 {
			return retryableFailure()
		}
		return tools.NewResult("ok")
	})

	if result.IsError || calls != 3 || retries.attempts != 3 || len(retries.errors) != 2 {
		t.Fatalf("result = %+v, calls = %d, retries = %+v", result, calls, retries)
	}
}

func TestTransientRetryGivesUpAndReturnsLastError(t *testing.T) {
	noToolBackoff(t)
	calls := 0
	result, retries := executeWithTransientRetry(context.Background(), "web_fetch", func() *tools.Result {
		calls++
		return retryableFailure()
	})

	if !result.IsError || calls != 3 || retries.attempts != 3 {
		t.Fatalf("result = %+v, calls = %d, retries = %+v", result, calls, retries)
	}
}

func TestTransientRetrySkipsNonRetryableErrors(t *testing.T) {
	noToolBackoff(t)
	calls := 0
	_, retries := executeWithTransientRetry(context.Background(), "exec", func() *tools.Result {
		calls++
		return tools.ErrorResult("command denied by safety policy")
	})

	if calls != 1 || retries.attempts != 1 || len(retries.errors) != 0 {
		t.Fatalf("calls = %d, retries = %+v", calls, retries)
	}
}

func TestTransientRetryStopsOnCanceledContext(t *testing.T) {
	orig := toolRetryBackoff
	toolRetryBackoff = []time.Duration{time.Hour}
	t.Cleanup(func() { toolRetryBackoff = orig })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	result, _ := executeWithTransientRetry(ctx, "web_fetch", func() *tools.Result {
		calls++
		return retryableFailure()
	})

	if calls != 1 || !result.IsError {
		t.Fatalf("calls = %d, result = %+v", calls, result)
	}
}
