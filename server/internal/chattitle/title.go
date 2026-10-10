package chattitle

import (
	"regexp"
	"strings"
	"unicode"
)

const deterministicTitleLimit = 30

var (
	markdownFence = regexp.MustCompile("```.*?```")
	markdownMarks = regexp.MustCompile("[#*`>~_]")
	markdownLink  = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	titleSpace    = regexp.MustCompile(`[[:space:]]+`)
	embedOnly     = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
)

// Derive mirrors the existing first-party deterministic title policy for
// channel-created Chats. Go slices valid Unicode code points rather than UTF-16
// code units, while preserving the same first-line, Markdown, whitespace, and
// 30-character single-ellipsis behavior.
func Derive(body string) string {
	// The first line with text wins; a line that is only a pasted image
	// ("![image.png](…)") is skipped so a screenshot ahead of the question does
	// not name the chat "image.png". An image-only body still falls back to it.
	line, embed := "", ""
	for _, candidate := range strings.Split(body, "\n") {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if strings.TrimSpace(embedOnly.ReplaceAllString(candidate, "")) == "" {
			if embed == "" {
				embed = candidate
			}
			continue
		}
		line = candidate
		break
	}
	if line == "" {
		line = embed
	}
	line = markdownFence.ReplaceAllString(line, " ")
	line = markdownMarks.ReplaceAllString(line, "")
	line = markdownLink.ReplaceAllString(line, "$1")
	line = strings.TrimSpace(titleSpace.ReplaceAllString(line, " "))
	if line == "" {
		return ""
	}
	runes := []rune(line)
	if len(runes) <= deterministicTitleLimit {
		return line
	}
	return strings.TrimRightFunc(string(runes[:deterministicTitleLimit-1]), unicode.IsSpace) + "…"
}
