package channels

import (
	"errors"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

func TestMediaDeliveryResult(t *testing.T) {
	t.Parallel()

	textErr := errors.New("text down")
	mediaErr := errors.New("upload down")
	failed := AllMediaFailed([]bus.MediaAttachment{{URL: "/a.png"}, {URL: "/b.png"}}, mediaErr)

	if err := MediaDeliveryResult(nil, nil); err != nil {
		t.Fatalf("no failures: got %v, want nil", err)
	}
	if err := MediaDeliveryResult(nil, textErr); !errors.Is(err, textErr) {
		t.Fatalf("text only: got %v, want textErr", err)
	}
	if _, ok := AsMediaDeliveryError(MediaDeliveryResult(nil, textErr)); ok {
		t.Fatal("text-only failure must not look like a media failure")
	}

	err := MediaDeliveryResult(failed, textErr)
	mde, ok := AsMediaDeliveryError(err)
	if !ok || len(mde.Failed) != 2 || !errors.Is(mde.TextErr, textErr) {
		t.Fatalf("got %#v, want 2 failures and textErr", err)
	}
	if !errors.Is(err, mediaErr) || !errors.Is(err, textErr) {
		t.Fatalf("errors.Is must reach both causes: %v", err)
	}
	for _, want := range []string{"/a.png", "/b.png", "upload down", "text down"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Error() = %q, missing %q", err.Error(), want)
		}
	}
}
