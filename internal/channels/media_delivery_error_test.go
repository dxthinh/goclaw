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
	media := []bus.MediaAttachment{{URL: "/a.png"}, {URL: "/b.png"}}
	failed := AllMediaFailed(media, mediaErr)

	if err := MediaDeliveryResult(media, nil, nil); err != nil {
		t.Fatalf("no failures: got %v, want nil", err)
	}

	textOnly := MediaDeliveryResult(nil, nil, textErr)
	if !errors.Is(textOnly, textErr) {
		t.Fatalf("text-only message: got %v, want textErr", textOnly)
	}
	if _, ok := AsMediaDeliveryError(textOnly); ok {
		t.Fatal("a text-only message must not look like a media failure")
	}

	// Every file arrived and only the text failed: the error must say so, or the
	// dispatcher would record delivered files as failed.
	filesFine := MediaDeliveryResult(media, nil, textErr)
	mde, ok := AsMediaDeliveryError(filesFine)
	if !ok || len(mde.Failed) != 0 || !errors.Is(filesFine, textErr) {
		t.Fatalf("text failed after files: got %#v", filesFine)
	}
	if !strings.Contains(filesFine.Error(), "text down") {
		t.Fatalf("Error() = %q", filesFine.Error())
	}

	err := MediaDeliveryResult(media, failed, textErr)
	mde, ok = AsMediaDeliveryError(err)
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
