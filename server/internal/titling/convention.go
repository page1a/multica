// Package titling is the prompt module for chat titles and issue titles.
//
// Both titles are written by a model, so the rules live here and are routed
// into the prompts that actually name things: the chat recap after the
// agent's reply (recap.go), and the agent brief (plus the quick-create field line that would
// otherwise contradict it). Nothing in this package rejects a title. A title
// the user typed, or one an agent was handed as an exact string, stays.
package titling

import "unicode"

// chineseGlossary is appended only when the opening message is Chinese.
// English (and Japanese, which mixes kana) never see these words, so a
// non-Chinese chat is not pulled into Chinese titles.
const chineseGlossary = `The opening message is Chinese. In the title, use these everyday Chinese words instead of the English product identifiers. Do not apply this list to a message that is not Chinese.
- issue → 任务
- agent → 智能体
- workspace → 工作区
- project → 项目
- autopilot → 自动化
- daemon → 守护进程
- runtime → 运行时
- a single agent execution (internally "run") → 运行
Keep these in English even in a Chinese title: skill, API, CLI, URL, and brand names.
Status keys (todo, in_progress, in_review, done, blocked, cancelled) are identifiers. Do not translate them, and do not put one in the title unless the topic is that status itself.

`

// IssueTitleBriefSection is the agent-brief section for titles an agent
// chooses. Quick-create's per-turn field line points here; it must not
// restate a different shape. Every other task kind reads the same rules from
// `multica issue create --help` (IssueTitleRules, DENE-1329).
const IssueTitleBriefSection = "## Title Style\n\n" + IssueTitleRules

// IssueTitleRules is the title convention without its brief heading, shared
// by the quick-create brief and `multica issue create --help`.
const IssueTitleRules = `When you create an issue and nobody has handed you the exact title string, write a title a list can scan: which project, then what the work is. This is a prompt rule, not a server check. A title the user dictated stays as they wrote it.

Shape: ` + "`{Project}: {what}`" + `

- Project is the display name from ` + "`## Project Context`" + ` (the bold name, or a ` + "`### Project:`" + ` heading). Copy it exactly. No project in that section means omit this segment and write only the work.
- If several projects are listed, use the one the request is about. If it is about more than one, use the first.
- The work is a short verb phrase: one job, not a sentence, not a slogan, and not two jobs joined by "+" or a parenthetical tag.
- Same language as the user's request. Do not translate it.
- No surrounding quotes, no "Title:" prefix, no trailing period.

When the title is Chinese, use the everyday product words, not the English identifiers:

- issue → 任务
- agent → 智能体
- workspace → 工作区
- project → 项目
- autopilot → 自动化
- daemon → 守护进程
- runtime → 运行时
- a single agent execution (internally "run") → 运行

Keep skill, API, CLI, URL, and brand names (Multica, GitHub, Slack, Claude, Codex, Cursor) in English. Status keys stay lowercase English (` + "`todo`" + `, ` + "`in_progress`" + `, ` + "`in_review`" + `, ` + "`done`" + `, ` + "`blocked`" + `, ` + "`cancelled`" + `) and stay out of the title unless the work is about that status.

Chat titles are a different shape, "{Project} · {topic}", and are written by the chat-title prompt, not by you. Do not restyle an existing issue title unless the task asks you to rename it.

`

// openingIsChinese reports whether the message is Chinese rather than
// Japanese. Han without kana is the signal; a Japanese opening keeps its
// own language and does not receive the Chinese glossary.
func openingIsChinese(s string) bool {
	han := false
	for _, r := range s {
		if unicode.In(r, unicode.Hiragana, unicode.Katakana) {
			return false
		}
		if unicode.Is(unicode.Han, r) {
			han = true
		}
	}
	return han
}
