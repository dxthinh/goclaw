package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// toolRetryBackoff is the wait before each extra attempt of a tool whose result is
// marked Retryable. Short on purpose: the agent loop is waiting, and an error that
// outlasts these is handed to the model, which can decide what to do.
var toolRetryBackoff = []time.Duration{500 * time.Millisecond, 2 * time.Second}

const toolRetryErrorPreviewMax = 200

// toolRetries describes extra attempts made for one tool call, for the tool span.
type toolRetries struct {
	attempts int
	errors   []string
}

// executeWithTransientRetry runs a tool and repeats it while it reports a transient
// error, returning the last result. The call stays one tool call for the model, for
// metrics and for loop detection.
func executeWithTransientRetry(ctx context.Context, toolName string, run func() *tools.Result) (*tools.Result, toolRetries) {
	result := run()
	retries := toolRetries{attempts: 1}
	for _, delay := range toolRetryBackoff {
		if !result.IsError || !result.Retryable {
			break
		}
		retries.errors = append(retries.errors, truncateStr(result.ForLLM, toolRetryErrorPreviewMax))
		select {
		case <-ctx.Done():
			return result, retries
		case <-time.After(delay):
		}
		slog.Info("tool.retry", "tool", toolName, "attempt", retries.attempts+1, "error", retries.errors[len(retries.errors)-1])
		result = run()
		retries.attempts++
	}
	return result, retries
}
