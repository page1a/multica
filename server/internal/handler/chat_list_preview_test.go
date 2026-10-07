package handler

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChatListPreview(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"short text stays", "hello  there\n\nfriend", "hello there friend"},
		{"code fence dropped", "see\n```go\nfunc main() {}\n```\nabove", "see above"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		if got := chatListPreview(c.in); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}

	long := strings.Repeat("中文回复", 2000)
	got := chatListPreview(long)
	if n := utf8.RuneCountInString(got); n != chatListPreviewRunes {
		t.Fatalf("long preview has %d runes, want %d", n, chatListPreviewRunes)
	}
	if !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Fatalf("long preview not clipped cleanly: %q", got)
	}
}
