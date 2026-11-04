package channels

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

// MediaFailure records one attachment a channel could not deliver.
type MediaFailure struct {
	Media bus.MediaAttachment
	Err   error
}

// MediaDeliveryError reports a partial Send: the channel attempted every
// attachment, and the ones in Failed did not reach the chat. Anything not listed
// was delivered, so the dispatcher can retry only the failures without
// re-sending files or text the user already has.
//
// TextErr is set when the text body failed as well. Text is never retried.
type MediaDeliveryError struct {
	Failed  []MediaFailure
	TextErr error
}

func (e *MediaDeliveryError) Error() string {
	parts := make([]string, 0, len(e.Failed)+1)
	for _, f := range e.Failed {
		parts = append(parts, fmt.Sprintf("%s: %v", f.Media.URL, f.Err))
	}
	if e.TextErr != nil {
		parts = append(parts, fmt.Sprintf("text: %v", e.TextErr))
	}
	return fmt.Sprintf("delivery failed for %d attachment(s): %s", len(e.Failed), strings.Join(parts, "; "))
}

// Unwrap exposes the underlying errors to errors.Is and errors.As.
func (e *MediaDeliveryError) Unwrap() []error {
	errs := make([]error, 0, len(e.Failed)+1)
	for _, f := range e.Failed {
		errs = append(errs, f.Err)
	}
	if e.TextErr != nil {
		errs = append(errs, e.TextErr)
	}
	return errs
}

// MediaDeliveryResult builds the return value of a channel Send that collected
// per-attachment failures. It returns nil when everything was delivered, and
// textErr alone when no attachment failed.
func MediaDeliveryResult(failed []MediaFailure, textErr error) error {
	if len(failed) == 0 {
		return textErr
	}
	return &MediaDeliveryError{Failed: failed, TextErr: textErr}
}

// AsMediaDeliveryError extracts a MediaDeliveryError from err's chain.
func AsMediaDeliveryError(err error) (*MediaDeliveryError, bool) {
	var mde *MediaDeliveryError
	if errors.As(err, &mde) {
		return mde, true
	}
	return nil, false
}

// AllMediaFailed marks every attachment as failed with the same cause.
func AllMediaFailed(media []bus.MediaAttachment, err error) []MediaFailure {
	failed := make([]MediaFailure, 0, len(media))
	for _, m := range media {
		failed = append(failed, MediaFailure{Media: m, Err: err})
	}
	return failed
}
