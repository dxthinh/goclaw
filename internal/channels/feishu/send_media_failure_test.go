package feishu

import (
	"context"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

// Text goes first on Feishu. Its failure used to return early, so the attachments
// were never attempted and could not be retried.
func TestSend_TextFailureStillAttemptsMedia(t *testing.T) {
	srv := newSimpleMockServer(t, `{"code":99991,"msg":"server busy"}`)
	ch := &Channel{
		BaseChannel: channels.NewBaseChannel("feishu-test", bus.New(), nil),
		client:      NewLarkClient("app", "secret", srv.URL),
	}
	ch.SetRunning(true)

	err := ch.Send(context.Background(), bus.OutboundMessage{
		ChatID:  "oc_chat_1",
		Content: "hello",
		Media:   []bus.MediaAttachment{{URL: "/no/such/file.pdf"}},
	})

	mde, ok := channels.AsMediaDeliveryError(err)
	if !ok || len(mde.Failed) != 1 || mde.TextErr == nil {
		t.Fatalf("Send error = %v, want the file attempted and both failures reported", err)
	}
}
