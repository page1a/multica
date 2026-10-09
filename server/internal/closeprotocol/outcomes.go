package closeprotocol

import (
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/issuestatus"
)

// Outcome is one row of the `issue close --outcome` table: the name the
// caller passes and what that outcome needs. The server is the only place
// this table lives (DENE-1183). The CLI forwards whatever it is given and
// relays the server's refusal, so a CLI shipped in an older desktop build
// can never refuse an outcome the server already accepts (DENE-978).
type Outcome struct {
	Name  string
	Needs string
}

// Outcomes is the full vocabulary. The first four are the original
// terminal/awaiting conclusions; backlog, todo and in_progress (DENE-1002)
// are deliberate non-terminal stops that still leave an evidence comment and
// a close.* record.
var Outcomes = []Outcome{
	{issuestatus.Done, "做完，要交付证据"},
	{issuestatus.InReview, "等验收，要 PR 或 --no-code"},
	{issuestatus.Blocked, "卡住，要写等什么"},
	{issuestatus.Cancelled, "取消"},
	{issuestatus.Backlog, "放回待规划，写一句为什么"},
	{issuestatus.Todo, "放回待办，写一句为什么"},
	{issuestatus.InProgress, "这轮先停、下一轮继续，要写谁继续"},
}

// OutcomeNames lists the outcome names in table order.
func OutcomeNames() []string {
	names := make([]string, len(Outcomes))
	for i, o := range Outcomes {
		names[i] = o.Name
	}
	return names
}

// IsOutcome reports whether name is a close outcome.
func IsOutcome(name string) bool {
	for _, o := range Outcomes {
		if o.Name == name {
			return true
		}
	}
	return false
}

// OutcomeHelp is the one-line "what each outcome needs" list the empty- and
// unknown-outcome refusals quote.
func OutcomeHelp() string {
	parts := make([]string, len(Outcomes))
	for i, o := range Outcomes {
		parts[i] = o.Name + "（" + o.Needs + "）"
	}
	return strings.Join(parts, "、")
}

// NormalizeOutcome is how the server reads a raw --outcome value.
func NormalizeOutcome(raw string) string { return strings.ToLower(strings.TrimSpace(raw)) }

// ContinuationRequiredMsg refuses an in_progress close that does not say who
// continues.
const ContinuationRequiredMsg = "放回进行中必须写明「接下来谁继续」，至少一种：--wake-at <RFC3339>（到点继续）、" +
	"--wait-condition \"...\" 配 --wait-timeout <RFC3339>（条件到了继续）、--blocked-by <票>（等这张票）、" +
	"--needs-human <member>（交给这个人）。只有一句原因、没人接着做的票请改用 --outcome backlog 或 --outcome todo。"

// VerdictRejectionMsg refuses any --verdict other than pass.
const VerdictRejectionMsg = "--verdict 只接受 pass。验收不通过不是收口：用 `multica issue comment add <id> --verdict hold --content-file <path>` 写明要改什么，票留在 in_review"

// OutcomeRejection is the refusal for a missing or unknown outcome, or "".
func OutcomeRejection(outcome string) string {
	if outcome == "" {
		return "缺 --outcome：" + OutcomeHelp()
	}
	if !IsOutcome(outcome) {
		return fmt.Sprintf("--outcome %q 不是收口结论；只能是 %s。%s", outcome, strings.Join(OutcomeNames(), " / "), OutcomeHelp())
	}
	return ""
}

// EvidenceRejection is the refusal for an empty evidence body, or "".
func EvidenceRejection(outcome, evidence string) string {
	if strings.TrimSpace(evidence) == "" {
		return "缺 --evidence：收口必须留证据（PR 链接、测试结论、或说明为什么" + outcome + "），一句话也行"
	}
	return ""
}

// VerdictRejection is the refusal for a verdict other than pass, or "".
func VerdictRejection(verdict string) string {
	if verdict != "" && verdict != "pass" {
		return VerdictRejectionMsg
	}
	return ""
}

// Continuation is the raw wait fields of a close. It is the shape check
// only; the full record (resolved tickets, parsed clocks) is built by the
// block-wait gate.
type Continuation struct {
	BlockedBy     string
	WakeAt        string
	WaitCondition string
	WaitTimeout   string
	NeedsHuman    string
}

// Present is blockwait.Record.Structured on the raw fields (pinned by
// TestContinuationPresentMatchesBlockWaitStructured): a clock, a
// wait with a deadline, another ticket, or a named person.
func (c Continuation) Present() bool {
	if strings.TrimSpace(c.BlockedBy) != "" || strings.TrimSpace(c.WakeAt) != "" || strings.TrimSpace(c.NeedsHuman) != "" {
		return true
	}
	return strings.TrimSpace(c.WaitCondition) != "" && strings.TrimSpace(c.WaitTimeout) != ""
}

// Request is the shape of a close before anything is looked up.
type Request struct {
	Outcome      string
	Evidence     string
	Verdict      string
	Continuation Continuation
	Knowledge    *KnowledgeAudit
	// DeliveredFiles is the delivery's changed paths, as the CLI read them
	// from git; nil when the caller sent none (see BindDeliveredFiles).
	DeliveredFiles *[]string
	// MemoryFiles are the delivery's project-memory files as git reads them
	// (DENE-1680); nil when the caller sent none. Boss marks a boss-layer
	// sediment round, which must declare an action per change.
	MemoryFiles *[]MemoryFile
	Boss        bool
}

// CheckRequest is the server's request-shape gate, in the order the close
// endpoint applies it. It needs no database, so `issue close` can ask for it
// before it does anything irreversible (a local merge). "" means the shape
// is acceptable; the close endpoint still runs its full gate.
func CheckRequest(req Request) string {
	outcome := NormalizeOutcome(req.Outcome)
	if msg := OutcomeRejection(outcome); msg != "" {
		return msg
	}
	if msg := EvidenceRejection(outcome, req.Evidence); msg != "" {
		return msg
	}
	if msg := VerdictRejection(strings.ToLower(strings.TrimSpace(req.Verdict))); msg != "" {
		return msg
	}
	if outcome == issuestatus.InProgress && !req.Continuation.Present() {
		return ContinuationRequiredMsg
	}
	if req.Knowledge == nil {
		return KnowledgeAuditRequiredMsg
	}
	audit, _, err := CanonicalKnowledgeAudit(StripKnowledgeEvidence(*req.Knowledge))
	if err != nil {
		return err.Error()
	}
	if KnowledgeMustShip(outcome, req.Verdict) {
		if _, err := BindDeliveredFiles(audit, req.DeliveredFiles); err != nil {
			return err.Error()
		}
		if err := CheckMemoryHygiene(audit, req.MemoryFiles, req.Boss); err != nil {
			return err.Error()
		}
	}
	return ""
}

// KnowledgeMustShip reports whether a close delivers work, so the knowledge it
// claims has to ride along: done and in_review by the executor. A reviewer's
// pass ships someone else's delivery, and a blocked or parked close ships
// nothing yet.
func KnowledgeMustShip(outcome, verdict string) bool {
	if strings.TrimSpace(verdict) != "" {
		return false
	}
	outcome = NormalizeOutcome(outcome)
	return outcome == issuestatus.Done || outcome == issuestatus.InReview
}
