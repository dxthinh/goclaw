package tools

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"
)

func TestIsTransientNetworkError(t *testing.T) {
	t.Parallel()
	dialErr := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"canceled", &url.Error{Op: "Get", URL: "http://x", Err: context.Canceled}, false},
		{"dial failure", &url.Error{Op: "Get", URL: "http://x", Err: dialErr}, true},
		{"redirect blocked by policy", &url.Error{Op: "Get", URL: "http://x", Err: errors.New("SSRF protection: private address")}, false},
		{"connection reset", errors.New("read: connection reset by peer"), true},
		{"upstream 503", errors.New("brave API returned 503: busy"), true},
		{"upstream 429", errors.New("tavily API returned 429: slow down"), true},
		{"bad key", errors.New("brave API returned 401: unauthorized"), false},
		{"bad request", errors.New("create request: invalid URL"), false},
	}
	for _, tc := range cases {
		if got := IsTransientNetworkError(tc.err); got != tc.want {
			t.Errorf("%s: IsTransientNetworkError(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}
