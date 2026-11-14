package media

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtFromMimeArchiveTypes(t *testing.T) {
	tests := []struct {
		mime string
		want string
	}{
		{mime: "application/zip", want: ".zip"},
		{mime: "application/x-zip-compressed", want: ".zip"},
		{mime: "application/x-tar", want: ".tar"},
		{mime: "application/gzip", want: ".gz"},
	}

	for _, tt := range tests {
		t.Run(tt.mime, func(t *testing.T) {
			if got := ExtFromMime(tt.mime); got != tt.want {
				t.Fatalf("ExtFromMime(%q) = %q, want %q", tt.mime, got, tt.want)
			}
		})
	}
}

func TestLoadSessionPathIsolation(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "upload.png")
	if err := os.WriteFile(src, []byte("uploaded image"), 0600); err != nil {
		t.Fatal(err)
	}
	id, saved, err := store.SaveFile("user-a/session-a", src, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.LoadSessionPath("user-a/session-a", id); err != nil || got != saved {
		t.Fatalf("path=%q error=%v, want %q", got, err, saved)
	}
	for _, session := range []string{"user-a/session-b", "user-b/session-a", ""} {
		if _, err := store.LoadSessionPath(session, id); err == nil {
			t.Fatalf("cross-session lookup succeeded for %q", session)
		}
	}
	for _, invalid := range []string{"*", "../" + id, saved, "00000000-0000-0000-0000-000000000000"} {
		if _, err := store.LoadSessionPath("user-a/session-a", invalid); err == nil {
			t.Fatalf("invalid or unknown ID succeeded: %q", invalid)
		}
	}
	if err := os.Remove(saved); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private.png")
	if err := os.WriteFile(outside, []byte("private image"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadSessionPath("user-a/session-a", id); err == nil {
		t.Fatal("symlink outside media session was accessible")
	}
}
