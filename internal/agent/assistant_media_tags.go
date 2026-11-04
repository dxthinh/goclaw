package agent

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

const (
	// assistantMediaHeader introduces the runtime-only file list on a history
	// message. It is never persisted; stripAssistantMediaTags removes it if a
	// model echoes it into a reply.
	assistantMediaHeader = "[Files attached to this reply. To inspect one, call the matching read_* tool with its media_id.]"
	maxMediaDescRunes    = 200
)

// assistantMediaTagPattern matches only the tags built here: the desc attribute
// keeps user-sent <media:*> tags, which never carry it, out of the match.
var assistantMediaTagPattern = regexp.MustCompile(`^<media:[a-z]+ id="(?:[^"\\]|\\.)*" desc="(?:[^"\\]|\\.)*">$`)

// isAssistantMediaRef reports whether a ref came from the assistant. Providers
// only send images on user messages, and compaction can move assistant refs onto
// a user message, so both the message role and the ref origin count.
func isAssistantMediaRef(msg providers.Message, ref providers.MediaRef) bool {
	return msg.Role == "assistant" || ref.Origin == providers.MediaOriginAssistant
}

// preservedMediaRef returns a ref ready to move to another message during
// compaction, recording its origin before the source message's role is lost.
func preservedMediaRef(msg providers.Message, ref providers.MediaRef) providers.MediaRef {
	if msg.Role == "assistant" {
		ref.Origin = providers.MediaOriginAssistant
	}
	return ref
}

// enrichAssistantMediaTags lists assistant files as text on their message, so the
// model knows what it already sent and can choose what to read without the image
// bytes taking up context. messages is the in-memory request copy.
func enrichAssistantMediaTags(messages []providers.Message) {
	for i := range messages {
		if messages[i].Role != "user" && messages[i].Role != "assistant" {
			continue
		}
		var tags []string
		for _, ref := range messages[i].MediaRefs {
			if isAssistantMediaRef(messages[i], ref) {
				tags = append(tags, fmt.Sprintf("<media:%s id=%q desc=%q>", ref.Kind, ref.ID, assistantMediaDesc(ref)))
			}
		}
		if len(tags) == 0 {
			continue
		}
		block := assistantMediaHeader + "\n" + strings.Join(tags, "\n")
		if messages[i].Content != "" {
			block = messages[i].Content + "\n\n" + block
		}
		messages[i].Content = block
	}
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
	if !strings.Contains(content, "<media:") && !strings.Contains(content, assistantMediaHeader) {
		return content
	}
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == assistantMediaHeader || assistantMediaTagPattern.MatchString(trimmed) {
			continue
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}
