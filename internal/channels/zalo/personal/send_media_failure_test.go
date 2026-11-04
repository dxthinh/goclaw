package personal

import (
	"context"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/channels/zalo/personal/protocol"
)

// An upload failure used to be logged and swallowed, so Send reported success and
// the dispatcher never learned the file was lost.
func TestSend_UploadFailureReturnsMediaDeliveryError(t *testing.T) {
	ch := &Channel{
		BaseChannel: channels.NewBaseChannel("zp-test", bus.New(), nil),
		sess:        &protocol.Session{},
	}
	ch.SetRunning(true)

	err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID: "c1",
		Media:  []bus.MediaAttachment{{URL: "/no/such/a.png"}, {URL: "/no/such/b.pdf"}},
	})

	mde, ok := channels.AsMediaDeliveryError(err)
	if !ok || len(mde.Failed) != 2 {
		t.Fatalf("Send error = %v, want both attachments reported", err)
	}
}
