package titling

import (
	"strings"
	"testing"
	"unicode"
)

func TestChatTitleSystemPromptHasNoCJK(t *testing.T) {
	for _, r := range ChatTitleSystemPrompt {
		if unicode.Is(unicode.Han, r) || unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			t.Fatalf("system prompt contains CJK %q; that pulls non-Chinese chats into that language", string(r))
		}
	}
	for _, want := range []string{
		"{project} · {topic}",
		"SAME language as the user's message",
		"Do NOT prefix it with a label",
	} {
		if !strings.Contains(ChatTitleSystemPrompt, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
}

func TestChatTitleUserPromptRoutesThreeOpenings(t *testing.T) {
	cases := []struct {
		name     string
		projects []string
		source   string
		want     []string
		ban      []string
	}{
		{
			name:   "english, no project — message only",
			source: "why does login bounce between pages",
			want:   []string{"why does login bounce between pages"},
			ban:    []string{"Project:", "任务", "Opening message:"},
		},
		{
			name:     "chinese, one project — project plus glossary",
			projects: []string{"Multica 魔改"},
			source:   "登录之后一直在几个页面之间来回跳",
			want: []string{
				"Project: Multica 魔改",
				"issue → 任务",
				"agent → 智能体",
				"Opening message:\n登录之后一直在几个页面之间来回跳",
			},
		},
		{
			name:     "english, one project — project, no Chinese glossary",
			projects: []string{"Billing"},
			source:   "retry the failed invoices",
			want:     []string{"Project: Billing", "Opening message:\nretry the failed invoices"},
			ban:      []string{"任务", "智能体"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChatTitleUserPrompt(tc.projects, tc.source)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("prompt missing %q\n---\n%s", want, got)
				}
			}
			for _, ban := range tc.ban {
				if strings.Contains(got, ban) {
					t.Errorf("prompt contains %q\n---\n%s", ban, got)
				}
			}
		})
	}
}

func TestChatTitleUserPromptJapaneseSkipsChineseGlossary(t *testing.T) {
	got := ChatTitleUserPrompt(nil, "ログインが失敗する")
	if strings.Contains(got, "任务") {
		t.Fatalf("Japanese opening received the Chinese glossary:\n%s", got)
	}
	if got != "ログインが失敗する" {
		t.Fatalf("prompt = %q, want the message unchanged", got)
	}
}

func TestIssueTitleBriefSection(t *testing.T) {
	for _, want := range []string{
		"## Title Style",
		"`{Project}: {what}`",
		"issue → 任务",
		"agent → 智能体",
		"autopilot → 自动化",
		"{Project} · {topic}",
		"A title the user dictated stays",
	} {
		if !strings.Contains(IssueTitleBriefSection, want) {
			t.Errorf("issue title section missing %q", want)
		}
	}
}
