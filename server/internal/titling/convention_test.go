package titling

import (
	"strings"
	"testing"
	"unicode"
)

func TestRecapSystemPromptsHaveNoCJK(t *testing.T) {
	for name, prompt := range map[string]string{
		"title":    ChatRecapTitleSystemPrompt,
		"topic":    ChatTopicSystemPrompt,
		"progress": ChatProgressSystemPrompt,
	} {
		for _, r := range prompt {
			if unicode.Is(unicode.Han, r) || unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
				t.Fatalf("%s prompt contains CJK %q; that pulls non-Chinese chats into that language", name, string(r))
			}
		}
	}
	for _, want := range []string{
		"{project} · {topic}",
		"SAME language as the user's opening message",
		"Do NOT prefix it with a label",
		"take over this: <link>",
	} {
		if !strings.Contains(ChatRecapTitleSystemPrompt, want) {
			t.Errorf("recap title prompt missing %q", want)
		}
	}
	if !strings.Contains(ChatTopicSystemPrompt, TopicKeep) {
		t.Errorf("topic prompt must name the %q answer", TopicKeep)
	}
}

func TestChatRecapTitleUserPrompt(t *testing.T) {
	cases := []struct {
		name     string
		projects []string
		opening  string
		reply    string
		linked   []LinkedIssue
		want     []string
		ban      []string
	}{
		{
			name:    "english, no project",
			opening: "why does login bounce between pages",
			reply:   "The session cookie is dropped on redirect.",
			want: []string{
				"Opening message:\nwhy does login bounce between pages",
				"Assistant's first reply:\nThe session cookie is dropped on redirect.",
			},
			ban: []string{"Project:", "任务", "Linked issues:"},
		},
		{
			name:     "chinese, one project — project plus glossary",
			projects: []string{"Multica 魔改"},
			opening:  "登录之后一直在几个页面之间来回跳",
			reply:    "我先查重定向链路。",
			want:     []string{"Project: Multica 魔改", "issue → 任务", "agent → 智能体"},
		},
		{
			name:    "bare hand-over link — linked issue named",
			opening: "接管这个：mention://issue/0000",
			reply:   "收到，我来接。",
			linked:  []LinkedIssue{{Identifier: "DENE-12", Title: "登录跳转修复"}},
			want:    []string{"Linked issues:\n- DENE-12 登录跳转修复"},
		},
		{
			name:    "japanese — no chinese glossary",
			opening: "ログインが失敗する",
			reply:   "確認します",
			ban:     []string{"任务"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChatRecapTitleUserPrompt(tc.projects, tc.opening, tc.reply, tc.linked)
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

func TestRecapTurnPromptsOrderTurns(t *testing.T) {
	turns := []RecapTurn{{Role: "user", Content: "first"}, {Role: "assistant", Content: "second"}}
	for _, got := range []string{ChatTopicUserPrompt("T", turns), ChatProgressUserPrompt("T", turns)} {
		if !strings.Contains(got, "User: first\nAssistant: second") {
			t.Errorf("turns not rendered oldest first:\n%s", got)
		}
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
