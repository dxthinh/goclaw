package channels

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

// deliveryRetryBackoff is the wait before each retry of failed attachments.
//
// Retries run inline on the conversation's shard so a file cannot arrive after
// the next message in the same chat. The cost is that other chats hashed to this
// shard (see defaultOutboundShards) wait for the sum of these delays plus the retry
// sends. Those stay short: a slow failure (timeout) is never retried, and channels
// that retry on their own (SendRetrier) are skipped.
var deliveryRetryBackoff = []time.Duration{time.Second, 3 * time.Second, 9 * time.Second}

// errChannelNotRegistered marks attachments whose channel was removed or failed to
// register between enqueue and dispatch. Nothing was attempted.
var errChannelNotRegistered = errors.New("channel is not registered")

// DeliveryOutcome is the final result of delivering one attachment.
type DeliveryOutcome struct {
	Msg      bus.OutboundMessage
	Media    bus.MediaAttachment
	Attempts int
	Err      error // nil when the attachment reached the chat
}

// DeliveryRecorder receives the final outcome of every outbound attachment.
// Implementations must not block: they run on the outbound shard.
type DeliveryRecorder interface {
	RecordDelivery(ctx context.Context, outcome DeliveryOutcome)
}

// SetDeliveryRecorder sets the sink for attachment delivery outcomes.
func (m *Manager) SetDeliveryRecorder(r DeliveryRecorder) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deliveryRecorder = r
}

// SendRetrier is implemented by channels whose Send already retries transient
// failures of each attachment. The dispatcher does not retry their attachments
// again: the channel's own retry budget is spent, and another loop on top of it
// holds the conversation's shard for several of those budgets.
type SendRetrier interface {
	RetriesTransientSends() bool
}

// isTransientSendError reports whether a failed attachment is worth sending again:
// the platform turned it away for now (rate limit, overload, 5xx), or the
// connection failed before the upload began. Anything else, such as a file over
// the platform limit or a rejected chat, fails the same way every time.
func isTransientSendError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, ErrMediaUnsupported) {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true // nothing was sent
	}
	if mayHaveReachedChat(err) {
		return false
	}
	return isTransientFailureMessage(err.Error())
}

// mayHaveReachedChat reports a failure that can happen after the platform received
// the file: a timeout, reset or EOF while waiting for the answer. The file may
// already be in the chat, and a retry would send it twice.
func mayHaveReachedChat(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	// Channels often flatten these into text before returning them.
	lower := strings.ToLower(err.Error())
	for _, marker := range []string{"timeout", "timed out", "deadline exceeded", "connection reset", "broken pipe", "eof"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func channelRetriesSends(channel Channel) bool {
	r, ok := channel.(SendRetrier)
	return ok && r.RetriesTransientSends()
}

// channelHealthFailed reports whether the channel declared itself down. Retrying
// against it would only hold the shard while the backlog of other chats grows.
func channelHealthFailed(channel Channel) bool {
	reporter, ok := channel.(interface{ HealthSnapshot() ChannelHealth })
	return ok && reporter.HealthSnapshot().State == ChannelHealthStateFailed
}

// mediaAttempt tracks one attachment through the retry loop.
type mediaAttempt struct {
	media    bus.MediaAttachment
	attempts int
	err      error
}

// settleDelivery handles the result of the first Send: it retries what can be
// retried, reports what is still undelivered, and records every outcome.
func (m *Manager) settleDelivery(ctx context.Context, channel Channel, msg bus.OutboundMessage, sendErr error) {
	if sendErr == nil {
		for _, media := range msg.Media {
			m.recordDelivery(ctx, msg, media, 1, nil)
		}
		return
	}

	mde, partial := AsMediaDeliveryError(sendErr)
	if !partial {
		// The channel cannot say which files arrived, and it may have sent text:
		// a retry could duplicate them, so report the failure as is.
		for _, media := range msg.Media {
			m.recordDelivery(ctx, msg, media, 1, sendErr)
		}
		m.handleSendFailure(ctx, channel, msg, sendErr)
		return
	}

	failedURLs := make(map[string]struct{}, len(mde.Failed))
	pending := make([]mediaAttempt, 0, len(mde.Failed))
	for _, f := range mde.Failed {
		failedURLs[f.Media.URL] = struct{}{}
		pending = append(pending, mediaAttempt{media: f.Media, attempts: 1, err: f.Err})
	}
	for _, media := range msg.Media {
		if _, isFailed := failedURLs[media.URL]; !isFailed {
			m.recordDelivery(ctx, msg, media, 1, nil)
		}
	}

	var undelivered []bus.MediaAttachment
	var lastErr error
	for _, a := range m.retryFailedMedia(ctx, channel, msg, pending) {
		m.recordDelivery(ctx, msg, a.media, a.attempts, a.err)
		if a.err != nil {
			undelivered = append(undelivered, a.media)
			lastErr = a.err
		}
	}

	if lastErr == nil {
		lastErr = mde.TextErr
	}
	if lastErr != nil {
		report := msg
		report.Media = undelivered
		m.handleSendFailure(ctx, channel, report, lastErr)
	}
}

// retryFailedMedia re-sends attachments that failed transiently and returns the
// final state of every attachment. Each round sends all remaining files in one
// media-only message, so the text is never repeated and the total wait stays
// bounded by deliveryRetryBackoff however many files failed.
func (m *Manager) retryFailedMedia(ctx context.Context, channel Channel, msg bus.OutboundMessage, attempts []mediaAttempt) []mediaAttempt {
	if channelHealthFailed(channel) || channelRetriesSends(channel) {
		return attempts
	}

	for _, delay := range deliveryRetryBackoff {
		retry := make([]bus.MediaAttachment, 0, len(attempts))
		retryIdx := make(map[string]int, len(attempts))
		for i, a := range attempts {
			if a.err != nil && isTransientSendError(a.err) {
				retry = append(retry, a.media)
				retryIdx[a.media.URL] = i
			}
		}
		if len(retry) == 0 {
			break
		}

		select {
		case <-ctx.Done():
			return attempts
		case <-time.After(delay):
		}

		err := channel.Send(ctx, bus.OutboundMessage{
			Channel:          msg.Channel,
			ChatID:           msg.ChatID,
			Media:            retry,
			Metadata:         mediaRetryMeta(msg.Metadata),
			TenantID:         msg.TenantID,
			AgentID:          msg.AgentID,
			AgentOtherConfig: msg.AgentOtherConfig,
		})

		roundFailed := make(map[string]error, len(retry))
		if mde, ok := AsMediaDeliveryError(err); ok {
			for _, f := range mde.Failed {
				roundFailed[f.Media.URL] = f.Err
			}
		} else if err != nil {
			for _, media := range retry {
				roundFailed[media.URL] = err
			}
		}
		for _, media := range retry {
			a := &attempts[retryIdx[media.URL]]
			a.attempts++
			a.err = roundFailed[media.URL]
			slog.Info("delivery.retry", "channel", msg.Channel, "chat_id", msg.ChatID,
				"path", media.URL, "attempt", a.attempts, "error", a.err)
		}
	}
	return attempts
}

// MetaMediaRetry marks a media-only Send that re-delivers attachments of an earlier
// message. It runs seconds after that message, when the chat's placeholder, typing
// indicator or stream may belong to a newer run, so a channel must leave that state
// alone for such a Send.
const MetaMediaRetry = "media_retry"

// mediaRetryMeta keeps routing fields, so the file lands in the same thread, group
// and visibility as the first Send, plus what changes how a file is rendered. Reply
// and placeholder fields already served the first Send.
func mediaRetryMeta(orig map[string]string) map[string]string {
	meta := CopyFollowupRoutingMeta(orig)
	if v := orig["audio_as_voice"]; v != "" {
		meta["audio_as_voice"] = v
	}
	meta[MetaMediaRetry] = "true"
	return meta
}

func (m *Manager) recordDelivery(ctx context.Context, msg bus.OutboundMessage, media bus.MediaAttachment, attempts int, err error) {
	if err != nil {
		slog.Warn("delivery.failed", "channel", msg.Channel, "chat_id", msg.ChatID,
			"path", media.URL, "attempts", attempts, "error", err)
	}
	m.mu.RLock()
	recorder := m.deliveryRecorder
	m.mu.RUnlock()
	if recorder != nil {
		recorder.RecordDelivery(ctx, DeliveryOutcome{Msg: msg, Media: media, Attempts: attempts, Err: err})
	}
}
