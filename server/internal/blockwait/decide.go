package blockwait

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Patrol actions. Hold means the wait is still legitimate.
const (
	ActionHold    = "hold"
	ActionWake    = "wake"
	ActionRelease = "release"
	// ActionSeat is the in_review round whose acceptance seat is empty: fill
	// the seat and start it, or turn the wait into a structured block a person
	// can see. Waking the executor is never the answer here (DENE-869).
	ActionSeat = "seat"
)

// BlockerView is the live state of one issue this record waits on.
type BlockerView struct {
	Ref      string
	Status   string
	Accepted bool
}

// Cleared reports whether the blocker no longer holds the waiter.
func (b BlockerView) Cleared() bool {
	switch b.Status {
	case "done", "cancelled":
		return true
	default:
		return b.Accepted
	}
}

// PatrolInput is one quiet blocked or in-review issue.
type PatrolInput struct {
	Status         string
	Quiet          time.Duration
	Record         Record
	Blockers       []BlockerView
	Now            time.Time
	LastPatrol     time.Time
	HasLastPatrol  bool
	HasPassComment bool
	ReleasedPass   bool
	// SegmentNudged is set after this blocked episode already produced a wake.
	SegmentNudged bool
	// ReviewNudged is set after this in_review round already produced a nudge.
	ReviewNudged bool
	// ReviewerHuman means the acceptance seat is a person. The patrol leaves a
	// comment and does not enqueue a run.
	ReviewerHuman bool
	// ReviewerEmpty means the acceptance slot was never answered: no seat and
	// not "none". A quiet in_review round with an empty slot is seated, not
	// nudged — a nudge would land on the executor, who can only say "请验收".
	ReviewerEmpty bool
}

// Decision is what the patrol or the acceptance hook should do, plus the
// sentence to leave on the issue.
type Decision struct {
	Action         string
	Reason         string
	Record         Record
	ConsumeWakeAt  bool
	ConsumeWait    bool
	ConsumeBlocker string
	MarkSegment    bool
	MarkReview     bool
	// CommentOnly leaves the sentence and does not enqueue a run.
	CommentOnly bool
	// Inherited names the red checks a merge decision let through because the
	// same checks are already red on the base branch (DENE-892). BaseBranch is
	// that branch.
	Inherited  []string
	BaseBranch string
}

// DecidePatrol picks a single next step for one stalled issue.
// A due clock, a cleared blocker, or a quiet unstructured block wakes once;
// the caller consumes that reason. in_review is not woken for a leftover
// execution clock. A pass is only the caller's structured verdict for this
// review round.
func DecidePatrol(in PatrolInput) Decision {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	recentPatrol := in.HasLastPatrol && in.Now.Sub(in.LastPatrol) < QuietAfter
	if recentPatrol {
		if in.Status == "blocked" && clockBecameDue(in) {
			return blockedWake(in, clockDecision())
		}
		return Decision{Action: ActionHold}
	}

	if in.Status == "blocked" {
		for _, blocker := range in.Blockers {
			if blocker.Cleared() {
				return blockedWake(in, Decision{
					Action:         ActionWake,
					Reason:         fmt.Sprintf("挡路的 %s 已经解除（%s），叫醒等待方继续。", blocker.Ref, blocker.Status),
					ConsumeBlocker: blocker.Ref,
					MarkSegment:    true,
				})
			}
		}
		if in.Record.HasWakeAt && !in.Now.Before(in.Record.WakeAt) {
			return blockedWake(in, clockDecision())
		}
		if in.Record.HasWaitTimeout && !in.Now.Before(in.Record.WaitTimeout) {
			what := in.Record.WaitCondition
			if what == "" {
				what = "外部条件"
			}
			return blockedWake(in, Decision{
				Action:      ActionWake,
				Reason:      fmt.Sprintf("等「%s」已经过了截止时间，叫醒执行人复查。", what),
				ConsumeWait: true,
				MarkSegment: true,
			})
		}
		if in.Quiet >= QuietAfter && !in.Record.Structured() && !in.SegmentNudged {
			return blockedWake(in, Decision{
				Action:      ActionWake,
				Reason:      "这张票标了阻塞，但没写在等什么，也没有运行。平台把它接回来。",
				MarkSegment: true,
			})
		}
		if in.Quiet >= QuietAfter && waitingOnOpen(in) {
			return Decision{Action: ActionHold, Reason: "挡路的票还没结束。"}
		}
		return Decision{Action: ActionHold}
	}

	if in.Status == "in_review" {
		if in.HasPassComment || in.ReleasedPass {
			return Decision{Action: ActionRelease, Reason: "验收已经通过，但票还停在待验收。平台按通过收口。"}
		}
		if in.Quiet >= QuietAfter && in.ReviewerEmpty {
			return Decision{
				Action: ActionSeat,
				Reason: "待验收超过 30 分钟，但这张票没有验收席，执行人不是该被提醒的人。",
			}
		}
		if in.Quiet >= QuietAfter && !in.ReviewNudged {
			d := Decision{
				Action:      ActionWake,
				Reason:      "待验收超过 30 分钟没有新的结论，提醒验收人看一下。",
				MarkReview:  true,
				CommentOnly: in.ReviewerHuman || strings.TrimSpace(in.Record.NeedsHuman) != "",
			}
			return d
		}
	}
	return Decision{Action: ActionHold}
}

func clockBecameDue(in PatrolInput) bool {
	return in.Record.HasWakeAt && in.LastPatrol.Before(in.Record.WakeAt) && !in.Now.Before(in.Record.WakeAt)
}

func clockDecision() Decision {
	return Decision{
		Action:        ActionWake,
		Reason:        "到了预定的复查时间，叫醒执行人。",
		ConsumeWakeAt: true,
		MarkSegment:   true,
	}
}

func blockedWake(in PatrolInput, d Decision) Decision {
	if strings.TrimSpace(in.Record.NeedsHuman) != "" {
		d.CommentOnly = true
	}
	return d
}

func waitingOnOpen(in PatrolInput) bool {
	if len(in.Blockers) == 0 {
		return false
	}
	for _, blocker := range in.Blockers {
		if !blocker.Cleared() {
			return true
		}
	}
	return false
}

// PRSnapshot is the slice of pull-request state the release decision needs.
type PRSnapshot struct {
	Number    int
	State     string
	Mergeable string
	Checks    string
	URL       string
	// FailedChecks names the PR's own red checks; RunningChecks counts the
	// ones that have not finished.
	FailedChecks  []string
	RunningChecks int
	// Base is the latest CI on the PR's base branch. Nil means it could not
	// be read, and every red check counts as this PR's own.
	Base *BaseChecks
}

// BaseChecks is the base branch's latest CI, reduced to the red check names.
type BaseChecks struct {
	Branch string
	Failed []string
}

// inheritedFailures reports the red checks a PR only carries over from its
// base branch (DENE-892). It holds when every red check of the PR is also red,
// by name, on the base, nothing is still running, and the PR is otherwise
// mergeable. A conflict, a branch rule, a pending check, or a missing baseline
// keeps the gate closed.
func inheritedFailures(pr PRSnapshot) ([]string, bool) {
	switch strings.ToLower(strings.TrimSpace(pr.Checks)) {
	case "failure", "error", "failing":
	default:
		return nil, false
	}
	switch strings.ToLower(strings.TrimSpace(pr.Mergeable)) {
	case "clean", "has_hooks", "unstable":
	default:
		return nil, false
	}
	if pr.RunningChecks > 0 || pr.Base == nil || len(pr.FailedChecks) == 0 {
		return nil, false
	}
	if len(newFailures(pr)) > 0 {
		return nil, false
	}
	return uniqueSorted(pr.FailedChecks), true
}

// newFailures lists the PR's red checks that are not red on the base.
func newFailures(pr PRSnapshot) []string {
	baseRed := map[string]bool{}
	if pr.Base != nil {
		for _, name := range pr.Base.Failed {
			baseRed[name] = true
		}
	}
	var fresh []string
	for _, name := range uniqueSorted(pr.FailedChecks) {
		if !baseRed[name] {
			fresh = append(fresh, name)
		}
	}
	return fresh
}

func uniqueSorted(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// InheritedSentence is the line every report of a let-through carries.
func InheritedSentence(base string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	if base == "" {
		base = "主线"
	}
	return fmt.Sprintf("因主线原有失败放行：%s（%s 最新一次 CI 上同名检查本来就是红的；按 job 比对，同一个 job 里 PR 新加的失败会被一起放过）。", strings.Join(names, "、"), base)
}

// letThrough records the inherited checks of one PR on the decision.
func (d *Decision) letThrough(pr PRSnapshot, names []string) {
	d.Inherited = uniqueSorted(append(d.Inherited, names...))
	if d.BaseBranch == "" && pr.Base != nil {
		d.BaseBranch = pr.Base.Branch
	}
}

// Release actions.
const (
	ReleaseDone  = "done"
	ReleaseMerge = "merge"
	ReleaseBlock = "block"
)

// DecideRelease says what an acceptance pass should do with the linked PRs.
// A pass never stays in in_review: no open PR closes the issue, a clean PR is
// merged, and a conflict or a red check becomes a structured block.
func DecideRelease(prs []PRSnapshot, now time.Time) Decision {
	if now.IsZero() {
		now = time.Now()
	}
	var open []PRSnapshot
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "open") {
			open = append(open, pr)
		}
	}
	if len(open) == 0 {
		return Decision{Action: ReleaseDone, Reason: "验收已经通过，没有还开着的 PR，这张票可以关了。"}
	}
	var through Decision
	for _, pr := range open {
		if names, ok := inheritedFailures(pr); ok {
			through.letThrough(pr, names)
			continue
		}
		if prBlocked(pr) {
			rec := Record{
				WaitCondition:  prBlockReason(pr),
				HasWaitTimeout: true,
				WaitTimeout:    now.Add(QuietAfter),
				HasWakeAt:      true,
				WakeAt:         now.Add(QuietAfter),
			}
			return Decision{
				Action: ReleaseBlock,
				Reason: fmt.Sprintf("验收已经通过，但 %s。先标成阻塞，到点再看，不继续停在待验收。", rec.WaitCondition),
				Record: rec,
			}
		}
	}
	label := prLabel(open[0])
	return Decision{
		Action:     ReleaseMerge,
		Reason:     fmt.Sprintf("验收已经通过，平台合并 %s 并关票。", label) + InheritedSentence(through.BaseBranch, through.Inherited),
		Record:     Record{HasWakeAt: true, WakeAt: now.Add(QuietAfter), WaitCondition: "验收已通过，等待合并 " + label},
		Inherited:  through.Inherited,
		BaseBranch: through.BaseBranch,
	}
}

// DecideClose is the gate on a direct move to done. An open linked PR that is
// cleanly mergeable and whose checks are green is merged by the caller; any
// other open PR becomes a structured block. No open PR allows the close.
func DecideClose(prs []PRSnapshot, now time.Time) Decision {
	if now.IsZero() {
		now = time.Now()
	}
	var open []PRSnapshot
	for _, pr := range prs {
		if strings.EqualFold(pr.State, "open") {
			open = append(open, pr)
		}
	}
	if len(open) == 0 {
		return Decision{Action: ReleaseDone}
	}
	var through Decision
	for _, pr := range open {
		if prReadyToMerge(pr) {
			continue
		}
		if names, ok := inheritedFailures(pr); ok {
			through.letThrough(pr, names)
			continue
		}
		rec := Record{
			WaitCondition:  prCloseBlockReason(pr),
			HasWaitTimeout: true,
			WaitTimeout:    now.Add(QuietAfter),
			HasWakeAt:      true,
			WakeAt:         now.Add(QuietAfter),
		}
		return Decision{
			Action: ReleaseBlock,
			Reason: fmt.Sprintf("这张票要关，但 %s。先改成阻塞，不标完成。", rec.WaitCondition),
			Record: rec,
		}
	}
	label := prLabel(open[0])
	reason := fmt.Sprintf("这张票要关，%s 能干净合并且检查是绿的。平台先合并，再关票。", label)
	if len(through.Inherited) > 0 {
		reason = fmt.Sprintf("这张票要关，%s 能合并，红的检查都不是它新带进来的。平台先合并，再关票。", label) + InheritedSentence(through.BaseBranch, through.Inherited)
	}
	return Decision{
		Action:     ReleaseMerge,
		Reason:     reason,
		Record:     Record{HasWakeAt: true, WakeAt: now.Add(QuietAfter), WaitCondition: "等待合并 " + label},
		Inherited:  through.Inherited,
		BaseBranch: through.BaseBranch,
	}
}

func prReadyToMerge(pr PRSnapshot) bool {
	switch strings.ToLower(strings.TrimSpace(pr.Mergeable)) {
	case "clean", "has_hooks":
	default:
		return false
	}
	switch strings.ToLower(strings.TrimSpace(pr.Checks)) {
	case "", "success", "passing", "neutral", "skipped":
		return true
	default:
		return false
	}
}

func prCloseBlockReason(pr PRSnapshot) string {
	if prBlocked(pr) {
		return prBlockReason(pr)
	}
	label := prLabel(pr)
	switch strings.ToLower(strings.TrimSpace(pr.Checks)) {
	case "pending", "expected", "queued", "in_progress":
		return label + " 的检查还没出结果"
	}
	switch strings.ToLower(strings.TrimSpace(pr.Mergeable)) {
	case "", "unknown":
		return label + " 还没确认能干净合并"
	}
	return label + " 现在不能直接合并"
}

func prBlocked(pr PRSnapshot) bool {
	switch strings.ToLower(pr.Mergeable) {
	case "dirty", "blocked", "behind":
		return true
	}
	switch strings.ToLower(pr.Checks) {
	case "failure", "error", "failing", "cancelled":
		return true
	}
	return false
}

func prBlockReason(pr PRSnapshot) string {
	label := prLabel(pr)
	switch strings.ToLower(pr.Mergeable) {
	case "dirty":
		return label + " 有合并冲突"
	case "behind":
		return label + " 分支落后，合不进去"
	case "blocked":
		return label + " 被分支保护挡住"
	}
	switch strings.ToLower(pr.Checks) {
	case "failure", "error", "failing":
		return label + " 的检查是红的" + redDetail(pr)
	case "cancelled":
		return label + " 的检查被取消了"
	}
	return label + " 现在合不进去"
}

// redDetail says which red checks the gate holds the PR for, and why a
// baseline did not excuse them.
func redDetail(pr PRSnapshot) string {
	if len(pr.FailedChecks) == 0 {
		return ""
	}
	if pr.Base == nil {
		return "（" + strings.Join(uniqueSorted(pr.FailedChecks), "、") + "；没拿到主线基线，按新失败处理）"
	}
	if fresh := newFailures(pr); len(fresh) > 0 {
		return "（新引入：" + strings.Join(fresh, "、") + "，" + baseName(pr.Base) + " 上同名检查不是红的）"
	}
	if pr.RunningChecks > 0 {
		return "（和 " + baseName(pr.Base) + " 相同的失败，但还有检查没跑完）"
	}
	return ""
}

func baseName(b *BaseChecks) string {
	if b == nil || b.Branch == "" {
		return "主线"
	}
	return b.Branch
}

func prLabel(pr PRSnapshot) string {
	if pr.URL != "" {
		return pr.URL
	}
	if pr.Number > 0 {
		return fmt.Sprintf("PR #%d", pr.Number)
	}
	return "PR"
}

var (
	// hintPhrases only suggest that a reviewer may have meant to pass. They
	// never merge or close.
	hintPhrases = []string{"验收通过", "通过验收", "等待合并", "待合并"}
	holdPhrases = []string{"验收不通过", "未通过", "不通过", "驳回", "阻断", "needs-work", "需要修改", "打回", "暂不合并"}
)

// Verdict reports a standalone acceptance marker. "hold" wins when both lines
// are present. Prose that merely contains the words is not a verdict.
func Verdict(body string) string {
	pass, hold := false, false
	for _, line := range strings.Split(body, "\n") {
		switch strings.ToLower(strings.TrimSpace(line)) {
		case VerdictPassLine:
			pass = true
		case VerdictHoldLine:
			hold = true
		}
	}
	if hold {
		return "hold"
	}
	if pass {
		return "pass"
	}
	return ""
}

// IsAcceptancePass reports whether the comment carries the pass marker on its
// own line. A hold marker wins.
func IsAcceptancePass(body string) bool {
	return Verdict(body) == "pass"
}

// IsAcceptanceHold reports whether the comment refuses the pass, either with
// the hold marker or with a refusal phrase. Refusal phrases do not themselves
// merge; they only stop a pass hint.
func IsAcceptanceHold(body string) bool {
	if Verdict(body) == "hold" {
		return true
	}
	lower := strings.ToLower(body)
	for _, phrase := range holdPhrases {
		if strings.Contains(lower, strings.ToLower(phrase)) {
			return true
		}
	}
	return false
}

// LooksLikePassHint reports prose that sounds like a pass but is not the
// marker. Callers may tell the reviewer how to write the marker. They must
// not merge.
func LooksLikePassHint(body string) bool {
	if IsAcceptancePass(body) || IsAcceptanceHold(body) {
		return false
	}
	lower := strings.ToLower(body)
	for _, phrase := range hintPhrases {
		if strings.Contains(lower, strings.ToLower(phrase)) {
			return true
		}
	}
	return false
}

// PassInRound reports a pass marker written at or after this review round
// started. A missing round matches nothing, so a previous round's comment
// cannot close the issue.
func PassInRound(body string, commentAt, roundAt time.Time) bool {
	if roundAt.IsZero() || commentAt.Before(roundAt) {
		return false
	}
	return IsAcceptancePass(body)
}

// AppendVerdict adds the standalone marker line. An empty verdict leaves the
// body unchanged. The marker is not repeated when it is already its own line.
func AppendVerdict(body, verdict string) (string, error) {
	verdict = strings.ToLower(strings.TrimSpace(verdict))
	if verdict == "" {
		return body, nil
	}
	if verdict != "pass" && verdict != "hold" {
		return "", fmt.Errorf("verdict 只能是 pass 或 hold")
	}
	if Verdict(body) == verdict {
		return body, nil
	}
	line := "verdict: " + verdict
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return line + "\n", nil
	}
	return body + "\n\n" + line + "\n", nil
}

// FollowUp is the metadata to write after a patrol acts. Drops are consumed
// reasons; sets are the nudge flags and whatever remains of a blocker list.
func (d Decision) FollowUp(now time.Time, blockedBy, waitingOn, woken string) (set map[string]string, drop []string) {
	set = map[string]string{}
	if now.IsZero() {
		now = time.Now()
	}
	if d.Action == ActionWake || d.Action == ActionRelease || d.Action == ActionSeat {
		set[KeyPatrolAt] = now.UTC().Format(time.RFC3339)
	}
	if d.ConsumeWakeAt {
		drop = append(drop, KeyWakeAt)
	}
	if d.ConsumeWait {
		drop = append(drop, KeyWaitTimeout, KeyWaitCondition, KeyWaitProbe)
	}
	if ref := strings.TrimSpace(d.ConsumeBlocker); ref != "" {
		nextBlocked, dropBlocked, dropWaiting := consumeBlocker(blockedBy, waitingOn, ref)
		if dropBlocked {
			drop = append(drop, KeyBlockedBy)
		} else {
			set[KeyBlockedBy] = nextBlocked
		}
		if dropWaiting {
			drop = append(drop, "close.waiting_on")
		}
		set[KeyWokenBy] = MarkWoken(woken, ref)
	}
	if d.MarkSegment {
		set[KeySegmentNudged] = "1"
	}
	if d.MarkReview {
		set[KeyReviewNudged] = "1"
	}
	return set, drop
}

func consumeBlocker(blockedBy, waitingOn, ref string) (next string, dropBlocked, dropWaiting bool) {
	var kept []string
	for _, token := range splitTokens(blockedBy) {
		if token != ref {
			kept = append(kept, token)
		}
	}
	next = strings.Join(kept, ",")
	dropBlocked = next == ""
	dropWaiting = strings.TrimSpace(waitingOn) == ref
	return next, dropBlocked, dropWaiting
}
