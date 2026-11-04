package pancake

import (
	"context"
	"fmt"
	"log/slog"
	"mime"
	"os"
	"path/filepath"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

// handleMediaAttachments uploads media files from an OutboundMessage and returns
// the attachment IDs. A failed upload does not stop the remaining files; it is
// returned as a MediaFailure so the caller can still send the rest and the text.
func (ch *Channel) handleMediaAttachments(ctx context.Context, msg bus.OutboundMessage) ([]string, []channels.MediaFailure) {
	var ids []string
	var failed []channels.MediaFailure
	for _, att := range msg.Media {
		if att.URL == "" {
			continue
		}

		id, err := ch.uploadMediaFile(ctx, att.URL, att.ContentType)
		if err != nil {
			slog.Warn("pancake: skipping media attachment on upload error",
				"page_id", ch.pageID, "url", att.URL, "err", err)
			failed = append(failed, channels.MediaFailure{Media: att, Err: err})
			continue
		}
		ids = append(ids, id)
	}

	return ids, failed
}

// uploadMediaFile opens a local file path and uploads it to the Pancake API.
// path must be absolute to prevent directory traversal via relative paths.
func (ch *Channel) uploadMediaFile(ctx context.Context, path string, contentType string) (string, error) {
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		return "", fmt.Errorf("pancake: media path must be absolute, got: %s", path)
	}
	f, err := os.Open(cleanPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	ct := contentType
	if ct == "" {
		ct = mimeTypeFromPath(cleanPath)
	}

	return ch.apiClient.UploadMedia(ctx, filepath.Base(cleanPath), f, ct)
}

// mimeTypeFromPath guesses the MIME type from the file extension.
func mimeTypeFromPath(path string) string {
	ext := filepath.Ext(path)
	if ext == "" {
		return "application/octet-stream"
	}
	mt := mime.TypeByExtension(ext)
	if mt == "" {
		return "application/octet-stream"
	}
	return mt
}
