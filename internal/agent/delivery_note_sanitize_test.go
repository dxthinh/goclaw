package agent

import (
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

// A note about an undelivered file lands in the session after the turn ended. It
// must survive history repair, or the agent never learns the file did not arrive.
func TestSanitizeHistoryKeepsDeliveryNote(t *testing.T) {
	note := `[System] File "final.zip" was not delivered to zp: chunk over 512K`
	msgs := []providers.Message{
		{Role: "user", Content: "make the zip"},
		{Role: "assistant", Content: "Here it is."},
		{Role: "user", Content: note},
		{Role: "user", Content: "where is the file?"},
	}

	cleaned, _ := sanitizeHistory(msgs)

	// Consecutive user messages merge into one: the note reaches the model
	// together with the user's next message.
	if len(cleaned) != 3 || !strings.Contains(cleaned[2].Content, note) || !strings.Contains(cleaned[2].Content, "where is the file?") {
		t.Fatalf("cleaned = %+v", cleaned)
	}
}
