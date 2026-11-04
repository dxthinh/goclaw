package slack

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

// A media retry runs seconds after its message. By then the placeholder stored
// for the thread may be a newer run's "Thinking..." bubble, which the retry must
// not delete. A first media-only send still cleans up its own placeholder.
func TestSend_MediaRetryKeepsPlaceholder(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		meta       map[string]string
		wantRemain bool
	}{
		"media retry":      {meta: map[string]string{channels.MetaMediaRetry: "true"}, wantRemain: true},
		"first media send": {meta: map[string]string{}, wantRemain: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ch := newSendTestChannel(t, &fakeSlackAPI{})
			key := threadPlaceholderKey("C1", "")
			ch.placeholders.Store(key, "1700000000.000200")

			err := ch.Send(context.Background(), bus.OutboundMessage{
				ChatID:   "C1",
				Media:    []bus.MediaAttachment{{URL: filepath.Join(t.TempDir(), "missing.png")}},
				Metadata: tc.meta,
			})

			if _, ok := channels.AsMediaDeliveryError(err); !ok {
				t.Fatalf("err = %v, want the missing file reported", err)
			}
			if _, remain := ch.placeholders.Load(key); remain != tc.wantRemain {
				t.Fatalf("placeholder kept = %v, want %v", remain, tc.wantRemain)
			}
		})
	}
}
