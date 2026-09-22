package logexport

import (
	"fmt"
	"strings"
	"time"
)

// maxClues bounds the "key clues" list so the summary stays a summary.
const maxClues = 5

// maxClueRunes keeps one pathological log line from swallowing the summary.
const maxClueRunes = 200

// Summarize renders the paste-into-AI summary that ships inside every bundle.
//
// It is deliberately part of the artifact rather than something the UI builds:
// the dialog, the CLI, and a bundle fetched from a git link must all read the
// same sentence, and a summary regenerated client-side would drift the moment
// either side changed.
func Summarize(b Bundle) string {
	var sb strings.Builder
	sb.WriteString("## AI 摘要\n\n")

	subject := b.Task.IssueIdentifier
	if subject == "" {
		subject = "任务 " + shortID(b.Task.ID)
	}
	fmt.Fprintf(&sb, "%s 的运行 %s", subject, shortID(b.Task.ID))
	if d := humanDuration(b.Task.Window.From, b.Task.Window.To); d != "" {
		fmt.Fprintf(&sb, " 用时 %s", d)
	}
	if outcome := outcomeText(b.Task.Status); outcome != "" {
		fmt.Fprintf(&sb, "，%s", outcome)
	}
	sb.WriteString("。\n\n")

	fmt.Fprintf(&sb, "- 范围: %s\n", b.Task.Scope.Label())
	if b.Task.AgentName != "" {
		fmt.Fprintf(&sb, "- Agent: %s\n", b.Task.AgentName)
	}
	if b.Task.Window.From != "" || b.Task.Window.To != "" {
		fmt.Fprintf(&sb, "- 时间窗: %s — %s\n", dash(b.Task.Window.From), dash(b.Task.Window.To))
	}
	fmt.Fprintf(&sb, "- 结构化日志: %d 条 · %d 次运行\n", b.EntryCount, b.RunCount)
	fmt.Fprintf(&sb, "- 退出码: %s\n", exitCodeText(b.Task.ExitCode))
	if b.Truncated {
		fmt.Fprintf(&sb, "- 注意: 条目超过 %d 条，仅保留最近的部分\n", DefaultMaxEntries)
	}

	if clues := collectClues(b); len(clues) > 0 {
		sb.WriteString("\n关键线索:\n")
		for _, c := range clues {
			fmt.Fprintf(&sb, "- %s\n", c)
		}
	}

	if b.Redaction.Complete {
		sb.WriteString("\n本产物已自动脱敏：不含 token、密码或环境变量值。可直接作为上下文喂给 AI。\n")
	} else {
		// Never write the reassuring sentence on a bundle that only got the
		// pattern pass: its whole value is that a reader can trust it, and an
		// untrue "已自动脱敏" is how a credential travels to an AI chat.
		note := b.Redaction.Note
		if note == "" {
			note = "运行环境不可读"
		}
		fmt.Fprintf(&sb, "\n注意：本产物脱敏不完整（%s），仅应用了 token/密码形态匹配，仍可能含环境变量值，请勿直接外发。\n", note)
	}
	return sb.String()
}

func collectClues(b Bundle) []string {
	var clues []string
	seen := map[string]struct{}{}
	add := func(s string) {
		s = firstLine(s)
		s = truncateRunes(s, maxClueRunes)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		clues = append(clues, s)
	}

	for _, r := range b.Runs {
		if r.Status == "completed" {
			continue
		}
		if r.FailureReason != "" {
			add(fmt.Sprintf("运行 %s: %s", shortID(r.TaskID), r.FailureReason))
		}
		add(r.Error)
	}
	for _, e := range b.Entries {
		if len(clues) >= maxClues {
			break
		}
		if e.Type == "error" {
			add(e.Content)
			continue
		}
		if e.Output != "" && LooksLikeFailureLine(firstLine(e.Output)) {
			add(e.Output)
		}
	}
	if len(clues) > maxClues {
		clues = clues[:maxClues]
	}
	return clues
}

func outcomeText(status string) string {
	switch status {
	case "completed":
		return "正常结束"
	case "failed":
		return "以失败结束"
	case "cancelled":
		return "被取消"
	case "":
		return ""
	default:
		return fmt.Sprintf("状态为 %s", status)
	}
}

func exitCodeText(code *int) string {
	if code == nil {
		return "未知"
	}
	return fmt.Sprintf("%d", *code)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

func dash(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// humanDuration renders the window as a coarse duration. Milliseconds are
// noise in a summary; anything under a second reads as "不到 1 秒".
func humanDuration(from, to string) string {
	start, err := time.Parse(time.RFC3339, from)
	if err != nil {
		return ""
	}
	end, err := time.Parse(time.RFC3339, to)
	if err != nil || end.Before(start) {
		return ""
	}
	d := end.Sub(start)
	switch {
	case d < time.Second:
		return "不到 1 秒"
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分 %d 秒", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%d 小时 %d 分", int(d.Hours()), int(d.Minutes())%60)
	}
}
