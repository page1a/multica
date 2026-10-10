package routing

// PickReason answers "why this seat" in one word, and is the first line of
// every routing assignment comment that names an executor (DENE-1201). The
// set is closed: every place that explains a pick takes its word from here,
// so a reader learns four words once instead of reading prose each time.
// 接着做 joined in DENE-1202, 负载 in DENE-1203.
type PickReason string

const (
	// PickReasonQuote — an agent carried the person's own words, verified.
	PickReasonQuote PickReason = "原话"
	// PickReasonHuman — a hand placed it: a person, or an automation a person
	// configured.
	PickReasonHuman PickReason = "人工"
	// PickReasonTier — routing took it from the ladder: a confident verdict,
	// or a tier label a person put on the ticket.
	PickReasonTier PickReason = "档位"
	// PickReasonFallback — the verdict was too weak or missing, so the
	// ladder's fallback rung took it.
	PickReasonFallback PickReason = "兜底"
	// PickReasonContinuation — the seat that did the earlier, related work
	// continues this ticket (DENE-1202).
	PickReasonContinuation PickReason = "接着做"
	// PickReasonLoad — the ladder's seat was busy, and a seat of the same
	// rung and direction with fewer runs took it (DENE-1203).
	PickReasonLoad PickReason = "负载"
)

// PickReasonLine is the comment's first line. Empty reason means no executor
// was written or held, and nothing is said.
func PickReasonLine(reason PickReason, detail string) string {
	if reason == "" {
		return ""
	}
	line := "**为什么是他**：" + string(reason)
	if detail != "" {
		line += " —— " + detail
	}
	return line + "\n\n"
}

// heldPickReason classifies an executor already in the slot by whose decision
// it is (see origin.go).
func heldPickReason(source string) PickReason {
	switch source {
	case SourceQuote:
		return PickReasonQuote
	case SourceRouter:
		return PickReasonTier
	}
	return PickReasonHuman
}

// executorPickReason is the reason for the executor in one assignment
// comment, and the detail sentence that goes with it.
func executorPickReason(issue Issue, needExecutor bool, executor *Seat, executorSource string, raised bool) (PickReason, string) {
	if !needExecutor {
		return heldPickReason(issue.AssigneeSource), heldExecutorLine(issue)
	}
	if executor == nil {
		return "", ""
	}
	switch executorSource {
	case pickLabel:
		return PickReasonTier, "票上的「" + executor.TierLabel + "」标签"
	case pickContinuation:
		return PickReasonContinuation, executor.Continues
	case pickLoad:
		return PickReasonLoad, executor.Balanced
	case pickLearned:
		return PickReasonTier, executor.Learned
	case pickFallback:
		return PickReasonFallback, "判断不够确定或没有判断，落到兜底档"
	}
	if raised {
		return PickReasonTier, "按规则下限抬到的" + executor.TierLabel + "档"
	}
	return PickReasonTier, "规则表定的" + executor.TierLabel + "档"
}
