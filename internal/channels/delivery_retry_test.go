package channels

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

// scriptedChannel answers each Send from a script and records every message.
type scriptedChannel struct {
	*mockChannel
	mu      sync.Mutex
	script  []func(bus.OutboundMessage) error
	sends   []bus.OutboundMessage
	health  ChannelHealthState
	onRetry func()
}

func (c *scriptedChannel) Send(_ context.Context, msg bus.OutboundMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sends = append(c.sends, msg)
	if c.onRetry != nil && len(c.sends) == 1 {
		defer c.onRetry()
	}
	idx := len(c.sends) - 1
	if idx < len(c.script) {
		return c.script[idx](msg)
	}
	return nil
}

func (c *scriptedChannel) HealthSnapshot() ChannelHealth {
	return ChannelHealth{State: c.health}
}

type capturingRecorder struct {
	mu       sync.Mutex
	outcomes map[string]DeliveryOutcome
}

func (r *capturingRecorder) RecordDelivery(_ context.Context, o DeliveryOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.outcomes == nil {
		r.outcomes = make(map[string]DeliveryOutcome)
	}
	r.outcomes[o.Media.URL] = o
}

func failMedia(err error, urls ...string) func(bus.OutboundMessage) error {
	return func(msg bus.OutboundMessage) error {
		var failed []MediaFailure
		for _, u := range urls {
			for _, media := range msg.Media {
				if media.URL == u {
					failed = append(failed, MediaFailure{Media: media, Err: err})
				}
			}
		}
		return MediaDeliveryResult(msg.Media, failed, nil)
	}
}

func noBackoff(t *testing.T) {
	t.Helper()
	orig := deliveryRetryBackoff
	deliveryRetryBackoff = []time.Duration{0, 0, 0}
	t.Cleanup(func() { deliveryRetryBackoff = orig })
}

func newDeliveryFixture(script ...func(bus.OutboundMessage) error) (*Manager, *scriptedChannel, *capturingRecorder) {
	ch := &scriptedChannel{mockChannel: newMockChannel("zp", TypeZaloPersonal), script: script}
	mgr := NewManager(bus.New())
	mgr.channels["zp"] = ch
	rec := &capturingRecorder{}
	mgr.SetDeliveryRecorder(rec)
	return mgr, ch, rec
}

var errTransient = errors.New("connection reset by peer")

func TestDeliverOutbound_RetriesOnlyFailedMediaWithoutText(t *testing.T) {
	noBackoff(t)
	mgr, ch, rec := newDeliveryFixture(failMedia(errTransient, "/b.png"))

	mgr.deliverOutbound(context.Background(), bus.OutboundMessage{
		Channel: "zp", ChatID: "c1", Content: "done",
		Media: []bus.MediaAttachment{{URL: "/a.png"}, {URL: "/b.png"}},
		Metadata: map[string]string{
			"local_key":              "lk",
			"group_id":               "g1",
			"feishu_reply_target_id": "om_1",
			"bitrix_visibility":      "whisper",
			"reply_to_message_id":    "9",
			"placeholder_key":        "ph",
		},
	})

	if len(ch.sends) != 2 {
		t.Fatalf("sends = %d, want first send + 1 retry", len(ch.sends))
	}
	retry := ch.sends[1]
	if retry.Content != "" || len(retry.Media) != 1 || retry.Media[0].URL != "/b.png" {
		t.Fatalf("retry must carry only the failed file and no text: %+v", retry)
	}
	// The retry must reach the same thread, group and visibility as the first send,
	// without repeating first-send side effects.
	for key, want := range map[string]string{
		"local_key": "lk", "group_id": "g1", "feishu_reply_target_id": "om_1", "bitrix_visibility": "whisper",
		"reply_to_message_id": "", "placeholder_key": "",
	} {
		if got := retry.Metadata[key]; got != want {
			t.Errorf("retry metadata %s = %q, want %q", key, got, want)
		}
	}
	if got := rec.outcomes["/a.png"]; got.Attempts != 1 || got.Err != nil {
		t.Fatalf("/a.png outcome = %+v", got)
	}
	if got := rec.outcomes["/b.png"]; got.Attempts != 2 || got.Err != nil {
		t.Fatalf("/b.png outcome = %+v", got)
	}
}

func TestDeliverOutbound_PermanentFailureNotRetriedAndReported(t *testing.T) {
	noBackoff(t)
	permanent := errors.New("inner error code 201: chunk over 512K")
	mgr, ch, rec := newDeliveryFixture(failMedia(permanent, "/b.png"))

	mgr.deliverOutbound(context.Background(), bus.OutboundMessage{
		Channel: "zp", ChatID: "c1", Content: "done",
		Media: []bus.MediaAttachment{{URL: "/a.png"}, {URL: "/b.png"}},
	})

	// First send, then the failure notice. No retry in between.
	if len(ch.sends) != 2 || len(ch.sends[1].Media) != 0 || !strings.Contains(ch.sends[1].Content, "⚠️") {
		t.Fatalf("sends = %+v, want one send + one notice", ch.sends)
	}
	if got := rec.outcomes["/b.png"]; got.Attempts != 1 || !errors.Is(got.Err, permanent) {
		t.Fatalf("/b.png outcome = %+v", got)
	}
}

func TestDeliverOutbound_GivesUpAfterBackoffExhausted(t *testing.T) {
	noBackoff(t)
	alwaysFail := failMedia(errTransient, "/a.png")
	mgr, ch, rec := newDeliveryFixture(alwaysFail, alwaysFail, alwaysFail, alwaysFail)

	mgr.deliverOutbound(context.Background(), bus.OutboundMessage{
		Channel: "zp", ChatID: "c1", Media: []bus.MediaAttachment{{URL: "/a.png"}},
	})

	// One send, three retries, one failure notice.
	if len(ch.sends) != 5 {
		t.Fatalf("sends = %d, want 5", len(ch.sends))
	}
	if got := rec.outcomes["/a.png"]; got.Attempts != 4 || got.Err == nil {
		t.Fatalf("outcome = %+v, want 4 attempts and an error", got)
	}
}

func TestDeliverOutbound_SkipsRetryWhenChannelHealthFailed(t *testing.T) {
	noBackoff(t)
	mgr, ch, rec := newDeliveryFixture(failMedia(errTransient, "/a.png"))
	ch.health = ChannelHealthStateFailed

	mgr.deliverOutbound(context.Background(), bus.OutboundMessage{
		Channel: "zp", ChatID: "c1", Media: []bus.MediaAttachment{{URL: "/a.png"}},
	})

	if len(ch.sends) != 2 {
		t.Fatalf("sends = %d, want first send + failure notice only", len(ch.sends))
	}
	if got := rec.outcomes["/a.png"]; got.Attempts != 1 || got.Err == nil {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestDeliverOutbound_CanceledContextStopsDuringBackoff(t *testing.T) {
	orig := deliveryRetryBackoff
	deliveryRetryBackoff = []time.Duration{time.Hour}
	t.Cleanup(func() { deliveryRetryBackoff = orig })

	ctx, cancel := context.WithCancel(context.Background())
	mgr, ch, rec := newDeliveryFixture(failMedia(errTransient, "/a.png"))
	ch.onRetry = cancel

	done := make(chan struct{})
	go func() {
		mgr.deliverOutbound(ctx, bus.OutboundMessage{
			Channel: "zp", ChatID: "c1", Media: []bus.MediaAttachment{{URL: "/a.png"}},
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deliverOutbound did not stop on canceled context")
	}
	if got := rec.outcomes["/a.png"]; got.Err == nil {
		t.Fatalf("outcome = %+v, want an error", got)
	}
}

func TestDeliverOutbound_OpaqueErrorIsNotRetried(t *testing.T) {
	noBackoff(t)
	mgr, ch, rec := newDeliveryFixture(func(bus.OutboundMessage) error { return errTransient })

	mgr.deliverOutbound(context.Background(), bus.OutboundMessage{
		Channel: "zp", ChatID: "c1", Content: "x", Media: []bus.MediaAttachment{{URL: "/a.png"}},
	})

	// The channel cannot say what it delivered, so re-sending could duplicate.
	if len(ch.sends) != 2 || ch.sends[1].Content == "x" {
		t.Fatalf("sends = %+v, want send + notice", ch.sends)
	}
	if got := rec.outcomes["/a.png"]; got.Attempts != 1 || got.Err == nil {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestDeliverOutbound_TextOnlyNotRecorded(t *testing.T) {
	noBackoff(t)
	mgr, _, rec := newDeliveryFixture()
	mgr.deliverOutbound(context.Background(), bus.OutboundMessage{Channel: "zp", ChatID: "c1", Content: "x"})
	if len(rec.outcomes) != 0 {
		t.Fatalf("outcomes = %v, want none", rec.outcomes)
	}
}

func TestIsTransientSendError(t *testing.T) {
	t.Parallel()
	transient := []error{errTransient, errors.New("HTTP 503"), errors.New("rate limit exceeded"), errors.New("request timeout")}
	for _, err := range transient {
		if !isTransientSendError(err) {
			t.Errorf("isTransientSendError(%v) = false", err)
		}
	}
	permanent := []error{nil, context.Canceled, ErrMediaUnsupported, errors.New("inner error code 201: Dung lượng chunk upload không được vượt quá 512K"), errors.New("file is too big")}
	for _, err := range permanent {
		if isTransientSendError(err) {
			t.Errorf("isTransientSendError(%v) = true", err)
		}
	}
}

// Files that reached the chat stay recorded as delivered when only the text fails:
// a false failure would add a misleading session note and invite a duplicate send.
func TestDeliverOutbound_TextFailureKeepsDeliveredFiles(t *testing.T) {
	noBackoff(t)
	textErr := errors.New("text rejected")
	mgr, ch, rec := newDeliveryFixture(func(msg bus.OutboundMessage) error {
		return MediaDeliveryResult(msg.Media, nil, textErr)
	})

	mgr.deliverOutbound(context.Background(), bus.OutboundMessage{
		Channel: "zp", ChatID: "c1", Content: "done",
		Media: []bus.MediaAttachment{{URL: "/a.png"}, {URL: "/b.png"}},
	})

	for _, url := range []string{"/a.png", "/b.png"} {
		if got := rec.outcomes[url]; got.Err != nil || got.Attempts != 1 {
			t.Fatalf("%s outcome = %+v, want delivered", url, got)
		}
	}
	if len(ch.sends) != 1 {
		t.Fatalf("sends = %+v, want no retry and no media notice", ch.sends)
	}
}

// A channel removed between enqueue and dispatch cannot take a notice, but the
// files must not vanish from the record.
func TestDeliverOutbound_UnknownChannelRecordsFiles(t *testing.T) {
	mgr, _, rec := newDeliveryFixture()

	mgr.deliverOutbound(context.Background(), bus.OutboundMessage{
		Channel: "removed", ChatID: "c1",
		Media: []bus.MediaAttachment{{URL: "/a.png"}},
	})

	if got := rec.outcomes["/a.png"]; !errors.Is(got.Err, errChannelNotRegistered) || got.Attempts != 0 {
		t.Fatalf("outcome = %+v, want a not-registered failure with no attempt", got)
	}
}

func TestDeliverOutbound_NoticeKeepsRouting(t *testing.T) {
	noBackoff(t)
	mgr, ch, _ := newDeliveryFixture(failMedia(errors.New("file is too big"), "/a.png"))

	mgr.deliverOutbound(context.Background(), bus.OutboundMessage{
		Channel: "zp", ChatID: "c1",
		Media:    []bus.MediaAttachment{{URL: "/a.png"}},
		Metadata: map[string]string{"group_id": "g1", "reply_to_message_id": "9"},
	})

	notice := ch.sends[len(ch.sends)-1]
	if notice.Metadata["group_id"] != "g1" || notice.Metadata["reply_to_message_id"] != "" {
		t.Fatalf("notice metadata = %v", notice.Metadata)
	}
}

func TestDeliverOutbound_NoNoticeUnderPublicComment(t *testing.T) {
	noBackoff(t)
	mgr, ch, rec := newDeliveryFixture(failMedia(ErrMediaUnsupported, "/a.png"))

	mgr.deliverOutbound(context.Background(), bus.OutboundMessage{
		Channel: "zp", ChatID: "c1", Content: "reply",
		Media:    []bus.MediaAttachment{{URL: "/a.png"}},
		Metadata: map[string]string{"reply_to_comment_id": "cmt-1"},
	})

	if len(ch.sends) != 1 {
		t.Fatalf("sends = %+v, want no notice under a comment", ch.sends)
	}
	if rec.outcomes["/a.png"].Err == nil {
		t.Fatal("the failure must still be recorded")
	}
}
