package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/tokencount"
)

func writeTestImage(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("\xff\xd8\xff\xe0image"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func imageRef(path, origin string) providers.MediaRef {
	return providers.MediaRef{ID: filepath.Base(path), Kind: "image", MimeType: "image/jpeg", Path: path, Origin: origin}
}

func TestReloadMediaSkipsAssistantImages(t *testing.T) {
	dir := t.TempDir()
	userImg := writeTestImage(t, dir, "user.jpg")
	botImg := writeTestImage(t, dir, "bot.jpg")
	msgs := []providers.Message{
		{Role: "user", Content: "photo", MediaRefs: []providers.MediaRef{imageRef(userImg, "")}},
		{Role: "assistant", Content: "sent", MediaRefs: []providers.MediaRef{imageRef(botImg, "")}},
		// Compaction moved an assistant ref onto this user message.
		{Role: "user", Content: "more", MediaRefs: []providers.MediaRef{imageRef(botImg, providers.MediaOriginAssistant), imageRef(userImg, "")}},
	}

	var loop Loop
	loop.reloadMediaForMessages(msgs, maxMediaReloadMessages)

	if len(msgs[0].Images) != 1 {
		t.Fatalf("user image not reloaded: %d", len(msgs[0].Images))
	}
	if len(msgs[1].Images) != 0 {
		t.Fatalf("assistant image reloaded: %d", len(msgs[1].Images))
	}
	if len(msgs[2].Images) != 1 {
		t.Fatalf("moved assistant ref reloaded: %d images", len(msgs[2].Images))
	}
}

// Assistant messages keep counting toward the window, so the user images sent to
// the provider stay the same as before they stopped being loaded.
func TestReloadMediaWindowStillCountsAssistantMessages(t *testing.T) {
	dir := t.TempDir()
	userImg := writeTestImage(t, dir, "user.jpg")
	botImg := writeTestImage(t, dir, "bot.jpg")
	msgs := []providers.Message{
		{Role: "user", MediaRefs: []providers.MediaRef{imageRef(userImg, "")}},
		{Role: "assistant", MediaRefs: []providers.MediaRef{imageRef(botImg, "")}},
	}

	var loop Loop
	loop.reloadMediaForMessages(msgs, 1)

	if len(msgs[0].Images) != 0 {
		t.Fatal("user image outside the window was reloaded")
	}
}

func TestReloadMediaDoesNotChargeBudgetForAssistantImages(t *testing.T) {
	dir := t.TempDir()
	botImg := writeTestImage(t, dir, "bot.jpg")
	refs := make([]providers.MediaRef, 24)
	for i := range refs {
		refs[i] = imageRef(botImg, "")
	}
	msgs := []providers.Message{{Role: "assistant", Content: "sent", MediaRefs: refs}}

	var loop Loop
	loop.reloadMediaForMessages(msgs, maxMediaReloadMessages)

	tokens, err := tokencount.NewBudgetCounter().CountMessages(msgs)
	if err != nil {
		t.Fatal(err)
	}
	if tokens >= tokencount.InlineMediaUnit {
		t.Fatalf("assistant images charged to budget: %d tokens", tokens)
	}
}

func TestEnrichAssistantMediaTags(t *testing.T) {
	long := strings.Repeat("ả", maxMediaDescRunes+50)
	msgs := []providers.Message{
		{Role: "user", Content: "photo", MediaRefs: []providers.MediaRef{imageRef("/w/user.jpg", "")}},
		{Role: "assistant", Content: "Done", MediaRefs: []providers.MediaRef{
			{ID: "a.png", Kind: "image", Path: "/w/a.png", Prompt: "a \"red\"\nfox", Caption: "ignored"},
			{ID: "b.png", Kind: "image", Path: "/w/b.png", Caption: "Ngày 1"},
			{ID: "c.zip", Kind: "document", Path: "/w/c.zip"},
			{ID: "d.png", Kind: "image", Path: "/w/d.png", Prompt: long},
		}},
		{Role: "user", Content: "", MediaRefs: []providers.MediaRef{imageRef("/w/moved.png", providers.MediaOriginAssistant)}},
	}

	enrichAssistantMediaTags(msgs)

	if msgs[0].Content != "photo" {
		t.Fatalf("user media was tagged: %q", msgs[0].Content)
	}
	for _, want := range []string{
		"Done\n\n" + assistantMediaHeader,
		`<media:image id="a.png" desc="a \"red\" fox">`,
		`<media:image id="b.png" desc="Ngày 1">`,
		`<media:document id="c.zip" desc="c.zip">`,
	} {
		if !strings.Contains(msgs[1].Content, want) {
			t.Errorf("missing %q in:\n%s", want, msgs[1].Content)
		}
	}
	if strings.Contains(msgs[1].Content, long) {
		t.Error("long description was not truncated")
	}
	if !strings.Contains(msgs[2].Content, `<media:image id="moved.png" desc="moved.png">`) {
		t.Errorf("moved assistant ref not tagged: %q", msgs[2].Content)
	}
}

func TestSanitizeStripsEchoedAssistantMediaTags(t *testing.T) {
	msgs := []providers.Message{{Role: "assistant", Content: "Đã gửi", MediaRefs: []providers.MediaRef{
		{ID: "a.png", Kind: "image", Path: "/w/a.png", Prompt: `a "red" fox`},
	}}}
	enrichAssistantMediaTags(msgs)

	if got := SanitizeAssistantContent(msgs[0].Content); got != "Đã gửi" {
		t.Fatalf("echoed tags survived: %q", got)
	}

	userTag := `Xem <media:image id="x" path="a.png">`
	if got := SanitizeAssistantContent(userTag); got != userTag {
		t.Fatalf("unrelated tag removed: %q", got)
	}
}

func TestPreservedMediaRefRecordsOrigin(t *testing.T) {
	ref := imageRef("/w/a.png", "")
	if got := preservedMediaRef(providers.Message{Role: "assistant"}, ref); got.Origin != providers.MediaOriginAssistant {
		t.Fatalf("assistant ref origin = %q", got.Origin)
	}
	if got := preservedMediaRef(providers.Message{Role: "user"}, ref); got.Origin != "" {
		t.Fatalf("user ref origin = %q", got.Origin)
	}
}

// Tags belong to the request copy only. History comes back through sanitizeHistory,
// which copies messages, so enriching the copy must leave stored content untouched.
func TestEnrichAssistantMediaTagsDoesNotTouchStoredHistory(t *testing.T) {
	stored := []providers.Message{
		{Role: "user", Content: "gửi ảnh"},
		{Role: "assistant", Content: "Đã gửi", MediaRefs: []providers.MediaRef{{ID: "a.png", Kind: "image", Path: "/w/a.png"}}},
	}

	request, _ := sanitizeHistory(stored)
	enrichAssistantMediaTags(request)

	if stored[1].Content != "Đã gửi" {
		t.Fatalf("stored history changed: %q", stored[1].Content)
	}
	if !strings.Contains(request[1].Content, "<media:image") {
		t.Fatalf("request copy has no tag: %q", request[1].Content)
	}
}
