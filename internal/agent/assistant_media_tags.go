package agent

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

const (
	// assistantMediaHeader introduces the runtime-only file list on a history
	// message. It is never persisted; stripAssistantMediaTags removes it if a
	// model echoes it into a reply.
	assistantMediaHeader = "[Files attached to this reply. To inspect one, call the matching read_* tool with its media_id.]"
	// movedUserMediaHeader lists user files that compaction moved onto an assistant
	// message, which no provider sends images on.
	movedUserMediaHeader = "[Files the user sent earlier. To inspect one, call the matching read_* tool with its media_id.]"
	maxMediaDescRunes    = 200

	// maxPreservedMediaRefs bounds the refs compaction keeps on a single message.
	maxPreservedMediaRefs = 30
)

// assistantMediaTagPattern matches only the tags built here: the desc attribute
// keeps user-sent <media:*> tags, which never carry it, out of the match.
var assistantMediaTagPattern = regexp.MustCompile(`<media:[a-z]+ id="(?:[^"\\]|\\.)*" desc="(?:[^"\\]|\\.)*">`)

// isAssistantMediaRef reports whether a ref came from the assistant. Compaction can
// move refs onto a message of either role, so a recorded origin outranks the role of
// the message carrying the ref.
func isAssistantMediaRef(msg providers.Message, ref providers.MediaRef) bool {
	switch ref.Origin {
	case providers.MediaOriginAssistant:
		return true
	case providers.MediaOriginUser:
		return false
	}
	return msg.Role == "assistant"
}

// preservedMediaRef returns a ref ready to move to another message during
// compaction, recording its origin before the source message's role is lost. A ref
// moved by an earlier compaction keeps the origin it already has.
func preservedMediaRef(msg providers.Message, ref providers.MediaRef) providers.MediaRef {
	if ref.Origin != "" {
		return ref
	}
	switch msg.Role {
	case "assistant":
		ref.Origin = providers.MediaOriginAssistant
	case "user":
		ref.Origin = providers.MediaOriginUser
	}
	return ref
}

// preservedMediaRefs returns the newest refs of messages about to be compacted, in
// chronological order, because media_id lookups let the last duplicate ID win.
func preservedMediaRefs(messages []providers.Message) []providers.MediaRef {
	var refs []providers.MediaRef
	for i := len(messages) - 1; i >= 0; i-- {
		for _, ref := range slices.Backward(messages[i].MediaRefs) {
			if len(refs) == maxPreservedMediaRefs {
				slices.Reverse(refs)
				return refs
			}
			refs = append(refs, preservedMediaRef(messages[i], ref))
		}
	}
	slices.Reverse(refs)
	return refs
}

// enrichAssistantMediaTags lists assistant files as text on their message, so the
// model knows what it already sent and can choose what to read without the image
// bytes taking up context. messages is the in-memory request copy.
func enrichAssistantMediaTags(messages []providers.Message) {
	for i := range messages {
		if messages[i].Role != "user" && messages[i].Role != "assistant" {
			continue
		}
		var sent, moved []string
		for _, ref := range messages[i].MediaRefs {
			tag := fmt.Sprintf("<media:%s id=%q desc=%q>", ref.Kind, ref.ID, assistantMediaDesc(ref))
			switch {
			case isAssistantMediaRef(messages[i], ref):
				sent = append(sent, tag)
			case messages[i].Role == "assistant":
				moved = append(moved, tag)
			}
		}
		messages[i].Content = appendMediaList(messages[i].Content, assistantMediaHeader, sent)
		messages[i].Content = appendMediaList(messages[i].Content, movedUserMediaHeader, moved)
	}
}

func appendMediaList(content, header string, tags []string) string {
	if len(tags) == 0 {
		return content
	}
	block := header + "\n" + strings.Join(tags, "\n")
	if content == "" {
		return block
	}
	return content + "\n\n" + block
}

// assistantMediaDesc picks the best known description of a file: the prompt that
// generated it, the caption it was sent with, or its file name.
func assistantMediaDesc(ref providers.MediaRef) string {
	desc := ref.Prompt
	if strings.TrimSpace(desc) == "" {
		desc = ref.Caption
	}
	if strings.TrimSpace(desc) == "" {
		desc = filepath.Base(ref.Path)
	}
	desc = strings.Join(strings.Fields(desc), " ")
	if utf8.RuneCountInString(desc) > maxMediaDescRunes {
		desc = string([]rune(desc)[:maxMediaDescRunes])
	}
	return desc
}

// stripAssistantMediaTags removes the runtime file list when a model copies it
// into its reply, so channel users never see internal media tags.
func stripAssistantMediaTags(content string) string {
	if !strings.Contains(content, "<media:") && !strings.Contains(content, assistantMediaHeader) && !strings.Contains(content, movedUserMediaHeader) {
		return content
	}
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		stripped := strings.ReplaceAll(strings.ReplaceAll(line, assistantMediaHeader, ""), movedUserMediaHeader, "")
		stripped = assistantMediaTagPattern.ReplaceAllString(stripped, "")
		if stripped == line {
			result = append(result, line)
			continue
		}
		// A line holding nothing else, or only its list marker, goes away entirely.
		if strings.Trim(stripped, " \t-*•") == "" {
			continue
		}
		result = append(result, strings.TrimRight(strings.ReplaceAll(stripped, "  ", " "), " \t"))
	}
	return strings.Join(result, "\n")
}
