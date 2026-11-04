package tools

import (
	"context"
	"errors"
	"net/url"
	"regexp"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

// upstreamBusyRe matches the "<service> API returned 503: ..." errors tools build
// from HTTP responses, which are plain strings rather than typed errors.
var upstreamBusyRe = regexp.MustCompile(`\b(?:returned|status|HTTP) (?:429|5\d\d)\b`)

// IsTransientNetworkError reports whether a network failure may pass on its own.
func IsTransientNetworkError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	// net/http wraps every client failure in url.Error, which itself counts as a
	// net.Error. Judge the cause, or a redirect blocked by SSRF policy would look
	// like a network problem.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return providers.IsRetryableError(err) || upstreamBusyRe.MatchString(err.Error())
}
