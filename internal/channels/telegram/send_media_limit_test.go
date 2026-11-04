package telegram

import (
	"os"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/config"
)

func TestOutboundMediaMaxBytes_OfficialAPIUsesUploadLimit(t *testing.T) {
	ch := &Channel{config: config.TelegramConfig{MediaMaxBytes: defaultMediaMaxBytes}}

	if got := ch.outboundMediaMaxBytes(); got != officialAPIOutboundMaxBytes {
		t.Fatalf("outboundMediaMaxBytes() = %d, want %d", got, officialAPIOutboundMaxBytes)
	}
}

func TestValidateOutboundMediaSize_AllowsVideoBelowOfficialUploadLimit(t *testing.T) {
	ch := &Channel{config: config.TelegramConfig{MediaMaxBytes: defaultMediaMaxBytes}}
	path := sparseFile(t, 43_500_000)

	if err := ch.validateOutboundMediaSize(path); err != nil {
		t.Fatalf("validateOutboundMediaSize() returned error for 43.5 MB file: %v", err)
	}
}

func TestValidateOutboundMediaSize_RejectsOfficialAPIUploadAboveLimit(t *testing.T) {
	ch := &Channel{config: config.TelegramConfig{MediaMaxBytes: 100 * 1024 * 1024}}
	path := sparseFile(t, officialAPIOutboundMaxBytes+1)

	err := ch.validateOutboundMediaSize(path)
	if err == nil {
		t.Fatal("validateOutboundMediaSize() returned nil, want size error")
	}
	if !strings.Contains(err.Error(), "outbound media too large for Telegram upload") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateOutboundMediaSize_LocalAPIServerAllowsLargerUploads(t *testing.T) {
	ch := &Channel{config: config.TelegramConfig{APIServer: "http://127.0.0.1:8081"}}
	path := sparseFile(t, 150*1024*1024)

	if err := ch.validateOutboundMediaSize(path); err != nil {
		t.Fatalf("validateOutboundMediaSize() returned error for local API upload: %v", err)
	}
}

func sparseFile(t *testing.T, size int64) string {
	t.Helper()

	file, err := os.CreateTemp(t.TempDir(), "telegram-media-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.Truncate(path, size); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	return path
}

func TestPrepareTelegramMediaItems_OversizedFileFailsAlone(t *testing.T) {
	ch := &Channel{config: config.TelegramConfig{MediaMaxBytes: defaultMediaMaxBytes}}
	tooBig := sparseFile(t, officialAPIOutboundMaxBytes+1)
	ok := sparseFile(t, 10)

	items, failed, leftover := ch.prepareTelegramMediaItems(bus.OutboundMessage{
		Content: "hello",
		Media:   []bus.MediaAttachment{{URL: tooBig}, {URL: ok}},
	})

	if len(failed) != 1 || failed[0].Media.URL != tooBig {
		t.Fatalf("failed = %+v, want only the oversized file", failed)
	}
	if len(items) != 1 || items[0].media.URL != ok {
		t.Fatalf("items = %+v, want the sendable file", items)
	}
	// The message body moves to the first file that can actually be sent.
	if !items[0].carriesText || items[0].undeliveredText() == "" || leftover != "" {
		t.Fatalf("body not carried by sendable file: %+v, leftover %q", items[0], leftover)
	}
}

func TestPrepareTelegramMediaItems_TextSurvivesWhenNoFileIsSendable(t *testing.T) {
	ch := &Channel{config: config.TelegramConfig{MediaMaxBytes: defaultMediaMaxBytes}}
	tooBig := sparseFile(t, officialAPIOutboundMaxBytes+1)

	items, failed, leftover := ch.prepareTelegramMediaItems(bus.OutboundMessage{
		Content: "hello",
		Media:   []bus.MediaAttachment{{URL: tooBig}},
	})

	if len(items) != 0 || len(failed) != 1 || leftover != "hello" {
		t.Fatalf("items %+v, failed %+v, leftover %q", items, failed, leftover)
	}
}

func TestTelegramMediaItem_PerFileCaptionIsNotUndeliveredText(t *testing.T) {
	item := telegramMediaSendItem{caption: "per-file caption"}
	if got := item.undeliveredText(); got != "" {
		t.Fatalf("undeliveredText() = %q, want empty for a per-file caption", got)
	}
}
