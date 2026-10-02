package titling

import (
	"strings"
)

// ChatRecapTitleSystemPrompt names a chat after the agent's first reply
// (DENE-1037). The user's opening alone is often a voice-input ramble or a
// bare link; the agent's reply says what the chat is actually about. It names no
// language and contains no CJK, so non-Chinese chats are not pulled into
// Chinese.
const ChatRecapTitleSystemPrompt = `You write a very short title for a chat, given the user's opening message, the assistant's first reply, sometimes linked issues, and sometimes a project name.

What to name:
- Name what the user wants done, combining the opening message with how the assistant understood it. Drop filler, hesitations, greetings and preamble.
- When the opening is only a link, only an issue key, or a hand-over such as "take over this: <link>", name the chat after what the link or linked issue is about, not after the words "take over".

Shape:
- When a project name is provided, or the chat is clearly about one named project, write "{project} · {topic}".
- Copy the project name exactly. Do not translate it.
- When several project names are provided, use the one the chat is about. If it is about more than one, use the first.
- When there is no project, write only "{topic}".
- "{topic}" is a few words naming the goal. Not a full sentence. Ideally under 12 words.

Rules:
- Output ONLY the title text — nothing else, no explanation.
- Write the title in the SAME language as the user's opening message, and in no other.
- Do NOT wrap the title in quotes or brackets.
- Do NOT prefix it with a label such as "Title:", in any language.
- Do NOT end with a period or any trailing punctuation.`

// ChatTopicSystemPrompt decides whether a chat drifted to a new topic.
const ChatTopicSystemPrompt = `You check whether a chat's title still fits, given the current title and the latest messages.

- If the latest messages still serve the same goal as the title, output exactly: KEEP
- Only if the conversation has clearly moved on to a different goal, output a new title in the same shape and language as the current title ("{project} · {topic}" when the current title has a project segment, otherwise "{topic}").
- Output ONLY "KEEP" or the new title text. No quotes, no label, no trailing punctuation.`

// ChatProgressSystemPrompt writes the chat's progress subtitle.
const ChatProgressSystemPrompt = `You write one short line saying where a chat stands right now, given its title and the latest messages.

- State the current progress: what has been done, or what is waiting on whom. Not the goal itself — the title already says that.
- One line, ideally under 30 words. No quotes, no label, no trailing period.
- Write in the SAME language as the user's messages.
- Output ONLY the line.`

// TopicKeep is the topic check's "title still fits" answer.
const TopicKeep = "KEEP"

// RecapTurn is one message handed to a recap prompt.
type RecapTurn struct {
	Role    string
	Content string
}

// LinkedIssue is an issue referenced by the opening message.
type LinkedIssue struct {
	Identifier string
	Title      string
}

const recapMessageMaxRunes = 1200

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func writeProjects(b *strings.Builder, projectNames []string) {
	names := make([]string, 0, len(projectNames))
	for _, name := range projectNames {
		if name = strings.TrimSpace(name); name != "" {
			names = append(names, name)
		}
	}
	switch len(names) {
	case 0:
	case 1:
		b.WriteString("Project: " + names[0] + "\n\n")
	default:
		b.WriteString("Projects: " + strings.Join(names, ", ") + "\n\n")
	}
}

// ChatRecapTitleUserPrompt builds the first-reply naming turn.
func ChatRecapTitleUserPrompt(projectNames []string, opening, reply string, linked []LinkedIssue) string {
	var b strings.Builder
	writeProjects(&b, projectNames)
	if openingIsChinese(opening) {
		b.WriteString(chineseGlossary)
	}
	if len(linked) > 0 {
		b.WriteString("Linked issues:\n")
		for _, issue := range linked {
			b.WriteString("- " + issue.Identifier + " " + strings.TrimSpace(issue.Title) + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("Opening message:\n" + clipRunes(opening, recapMessageMaxRunes) + "\n\n")
	b.WriteString("Assistant's first reply:\n" + clipRunes(reply, recapMessageMaxRunes))
	return b.String()
}

func writeTurns(b *strings.Builder, turns []RecapTurn) {
	b.WriteString("Latest messages (oldest first):\n")
	for _, turn := range turns {
		role := "User"
		if turn.Role == "assistant" {
			role = "Assistant"
		}
		b.WriteString(role + ": " + clipRunes(turn.Content, recapMessageMaxRunes/2) + "\n")
	}
}

// ChatTopicUserPrompt builds the topic-drift turn.
func ChatTopicUserPrompt(currentTitle string, turns []RecapTurn) string {
	var b strings.Builder
	b.WriteString("Current title: " + currentTitle + "\n\n")
	writeTurns(&b, turns)
	return b.String()
}

// ChatProgressUserPrompt builds the progress-line turn.
func ChatProgressUserPrompt(title string, turns []RecapTurn) string {
	var b strings.Builder
	b.WriteString("Chat title: " + title + "\n\n")
	writeTurns(&b, turns)
	return b.String()
}
