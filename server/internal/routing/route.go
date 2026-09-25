package routing

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/pkg/llm"
)

// Action is what a Route call did, for logging, the CLI, and tests.
type Action string

const (
	// ActionSkipped — routing is not active for this workspace, or the ticket
	// is one it never touches. No request, no write, no comment, no mention.
	ActionSkipped Action = "skipped"
	// ActionNoop — this status has no routing behaviour (in_progress, done,
	// cancelled, backlog), or there was nothing left to fill.
	ActionNoop Action = "noop"
	// ActionAssigned — the todo row wrote at least one slot. It reports a
	// write, never an attempt: callers batch on this value, and an "assigned"
	// that wrote nothing is a dispatch that failed silently.
	ActionAssigned Action = "assigned"
	// ActionDeclined — the todo row ran, the judge answered, and no slot was
	// written. Reason says why, slot by slot. Distinct from ActionNoop, which
	// means there was nothing to decide.
	ActionDeclined Action = "declined"
	// ActionHandedOff — the in-review row ran.
	ActionHandedOff Action = "handed_off"
	// ActionAdvised — the blocked row ran. Writes nothing.
	ActionAdvised Action = "advised"
	// ActionUnavailable — the model could not be reached.
	ActionUnavailable Action = "unavailable"
	// ActionWoken — the stale-review row put a stalled ticket back in front
	// of whoever accepts it. Writes no status.
	ActionWoken Action = "woken"
	// ActionCompleted — the stale-review row moved a ticket to done because
	// the reviewer had already passed it on the ticket. The only action in
	// this package that changes a status.
	ActionCompleted Action = "completed"
)

// Outcome reports what happened, so the CLI entry point and the hook entry
// point can be shown to produce the same result on the same ticket.
type Outcome struct {
	State  State
	Action Action
	// Reason is a short note for logs, scripts, and agents, not user copy. It
	// is never empty when Action reports that nothing was written.
	Reason string
	// ExecutorWritten is the seat this call put in the executor slot, if any.
	ExecutorWritten *Seat
	// ReviewerWritten is the reviewer this call wrote, if any. Empty Kind
	// means this call did not write the slot.
	ReviewerWritten ReviewerRef
	// Mentioned reports whether this call notified a person.
	Mentioned bool
	// Commented reports whether this call posted a routing comment.
	Commented bool
}

// Router is the module both entry points call. The HTTP hooks call Route
// directly as a function; the CLI subcommand reaches the same Route through a
// small endpoint. There is no second implementation and no subprocess: routing
// a ticket must not cost a process start.
type Router struct {
	Store   Store
	Judge   Judge
	Breaker *Breaker
	Ladder  Ladder
	Log     *slog.Logger
}

// New builds a Router with the shipped ladder and breaker.
func New(store Store, judge Judge) *Router {
	return &Router{Store: store, Judge: judge, Breaker: NewBreaker(), Ladder: DefaultLadder}
}

func (r *Router) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Route answers "given this status, who should be holding this ticket", and
// applies the answer. It is called on issue creation and on every status
// change, and it is the only entry point: adding behaviour for a status is a
// row in the table below, not another hook.
//
// Route never writes status, stage, description, labels, or children, never
// merges and never closes. The four things it may touch are the assignee, the
// reviewer property, its own comments, and the subscriber list — and the last
// one only so a mention actually notifies.
func (r *Router) Route(ctx context.Context, workspaceID, issueID string) (Outcome, error) {
	settings, issue, done, err := r.admit(ctx, workspaceID, issueID)
	if done != nil {
		return *done, err
	}

	// A child issue is execution-only. It may still receive an executor at
	// `todo`, but acceptance is a single parent-level handoff. Keep this guard
	// before the status switch so a manually assigned reviewer, a stale child
	// row, or a direct `issue route` call cannot start an independent review
	// chain.
	if issue.ParentIssueID != "" && issue.Status == "in_review" {
		return Outcome{State: settings.State(), Action: ActionNoop, Reason: "sub-issue has no acceptance route"}, nil
	}

	switch issue.Status {
	case "todo":
		return r.routeTodo(ctx, workspaceID, settings, issue, fillStarts)
	case "in_review":
		return r.routeInReview(ctx, workspaceID, settings, issue)
	case "blocked":
		return r.routeBlocked(ctx, workspaceID, settings, issue)
	case "in_progress", "done", "cancelled", "backlog":
		// in_progress: somebody is working, do not interrupt.
		// backlog: nobody intends to work on it yet.
		// done / cancelled: over.
		return Outcome{State: settings.State(), Action: ActionNoop, Reason: "status has no routing behaviour"}, nil
	default:
		// Unknown category. Fail closed: an unrecognised status is not a
		// licence to guess who should hold the ticket.
		return Outcome{State: settings.State(), Action: ActionNoop, Reason: "unknown status category " + issue.Status}, nil
	}
}

// RouteGroupNode is the create-time pass for an issue an alignment confirm
// just wrote as part of a group (DENE-812). Route alone cannot seat such a
// group: the root is created in_progress so it coordinates instead of running,
// and every sub-issue past stage 1 is created in backlog so it waits — two
// statuses the state table deliberately leaves alone. Without this pass a
// confirmed group kept whatever seats the preview happened to hold, and the
// rows the preview left empty stayed empty for good: the stage barrier woke
// nobody on an unheld root, and a promoted child reached todo only if
// somebody promoted it.
//
// It is the same todo row — same ladder, same judge, same fill-only-empty
// slots, same one decision comment per issue — with one difference per node
// kind, carried by the fill mode:
//
//   - a todo sub-issue (stage 1) is seated and its run starts, exactly as
//     Route would do it;
//   - a backlog sub-issue (a later stage) is seated and parked: nothing runs
//     from backlog, so the seat starts when its stage is promoted to todo;
//   - the root of a group is seated as the coordinator, reviewer slot
//     included, and its run is NOT started — the stage barrier and the
//     sub-issues' completions are what wake it.
//
// Anything else — a node somebody already moved on, a root without
// sub-issues — gets exactly what Route gives it.
func (r *Router) RouteGroupNode(ctx context.Context, workspaceID, issueID string) (Outcome, error) {
	settings, issue, done, err := r.admit(ctx, workspaceID, issueID)
	if done != nil {
		return *done, err
	}
	switch {
	case issue.ParentIssueID != "" && issue.Status == "backlog":
		return r.routeTodo(ctx, workspaceID, settings, issue, fillParked)
	case issue.ParentIssueID == "" && issue.HasChildren && issue.Status == "in_progress":
		return r.routeTodo(ctx, workspaceID, settings, issue, fillCoordinator)
	}
	return r.Route(ctx, workspaceID, issueID)
}

// admit is the prelude every entry point shares: the switch, the breaker, the
// issue itself, and the one ticket routing never touches. A non-nil Outcome
// is the answer and the caller returns it with the error as is.
func (r *Router) admit(ctx context.Context, workspaceID, issueID string) (Settings, Issue, *Outcome, error) {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return settings, Issue{}, &Outcome{State: StateOff, Action: ActionSkipped, Reason: "settings unreadable"}, err
	}
	state := settings.State()
	if !state.Active() {
		// Off and incomplete are the pre-existing code path, to the letter:
		// no request, no write, no comment, and no mention.
		return settings, Issue{}, &Outcome{State: state, Action: ActionSkipped, Reason: "routing not enabled"}, nil
	}

	// The breaker is checked before the issue is even loaded. While it is
	// cooling down the workspace is "ineffective": the reason belongs in the
	// settings section, and a ticket must not be told about it again.
	if open, _, reason := r.Breaker.Open(workspaceID); open {
		return settings, Issue{}, &Outcome{State: StateIneffective, Action: ActionSkipped, Reason: reason}, nil
	}

	issue, err := r.Store.Issue(ctx, workspaceID, issueID)
	if err != nil {
		return settings, Issue{}, &Outcome{State: state, Action: ActionSkipped, Reason: "issue unreadable"}, err
	}

	if issue.AssignedToHuman() && !humanHeldIsRoutable(issue) {
		// A person's ticket is a person's ticket. Nothing is filled, nothing
		// is said, nobody is pinged.
		return settings, issue, &Outcome{State: state, Action: ActionSkipped, Reason: "assignee is a person"}, nil
	}
	return settings, issue, nil, nil
}

// humanHeldIsRoutable carves ONE case out of "a person holds it, do not
// touch it": the ticket is awaiting acceptance and its reviewer slot has
// never been decided.
//
// The blanket rule was written for "a person is DOING this work". It stopped
// meaning that the moment the in-review row started handing tickets to
// people: the handoff makes the assignee a person, and from then on every
// later pass took the early return above — so a ticket that reached review
// before its reviewer slot was ever filled could never have it filled, could
// never be handed to anybody, and could never be swept. Eighteen tickets in
// one workspace were in exactly that state, all answering `skipped / assignee
// is a person` (DENE-712 B1).
//
// The carve-out is as narrow as the bug: in_review only, empty reviewer slot
// only. A person holding a ticket in any other status is untouched, and a
// person holding a ticket whose reviewer slot already holds a value is
// untouched too — there is nothing left to fill, so the fill-only rule has
// already closed and the only thing left to do would be to take the ticket
// away from them.
func humanHeldIsRoutable(issue Issue) bool {
	return issue.Status == "in_review" && issue.Reviewer.Empty()
}

// fillMode is what an executor seat written by routeTodo does next.
type fillMode int

const (
	// fillStarts — the todo row proper: the assignment starts the seat's run.
	fillStarts fillMode = iota
	// fillParked — a later-stage sub-issue: seated now, runs when its stage is
	// promoted to todo. Nothing runs from backlog, so the write starts nothing.
	fillParked
	// fillCoordinator — the root of a group: seated to supervise its
	// sub-issues, and its run is not started by the write.
	fillCoordinator
)

// routeTodo is the only row that fills slots.
func (r *Router) routeTodo(ctx context.Context, workspaceID string, settings Settings, issue Issue, mode fillMode) (Outcome, error) {
	// Declined until a write proves otherwise: the action is derived from what
	// was written, at the bottom of this function.
	out := Outcome{State: StateEnabled, Action: ActionDeclined}

	needExecutor := issue.AssigneeType == ""
	needReviewer := issue.ParentIssueID == "" && issue.Reviewer.Empty()

	if !needExecutor && !needReviewer {
		// Both slots already hold a value, whoever wrote them. Repeated status
		// flips land here: nothing to fill, so nothing is asked, written, or
		// said. This is where idempotence comes from — no bookkeeping, just
		// the emptiness of the slots.
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "no empty slot"}, nil
	}

	ladder := r.Ladder.WithProjects(settings.Projects)
	match := ladder.ResolveDirection(issue.ProjectName)
	direction := match.Direction
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "roster unreadable"}, err
	}
	candidates := ladder.Candidates(direction, roster)
	if len(candidates) == 0 {
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "ladder has no seat in this workspace"}, nil
	}
	eligible, state, err := r.decisionContext(ctx, workspaceID, settings, issue, direction, candidates)
	if err != nil {
		return Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "routing context incomplete"}, err
	}
	if len(eligible) == 0 {
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "no eligible seat"}, nil
	}

	// A tier label on the ticket is an answer, not a hint: when it names a
	// rung that exists AND that seat can take work, the executor slot is
	// filled from it and the judge is never asked about strength. A dead
	// labelled seat is not an answer — the judge chooses among the seats
	// that are still eligible.
	requestedTier, labelled := r.Ladder.RequestedTier(issue.Labels)
	labelSeat, labelSeatOK := Seat{}, false
	if labelled {
		labelSeat, labelSeatOK = SeatByTier(eligible, requestedTier)
	}
	executorFromLabel := needExecutor && labelSeatOK

	var verdict Verdict
	if needReviewer || !executorFromLabel {
		v, err := r.Judge.Assign(ctx, settings.Target(), state)
		if err != nil {
			return r.reportUnavailable(ctx, workspaceID, issue, err)
		}
		r.Breaker.Succeed(workspaceID)
		verdict = v
	}

	threshold := settings.Threshold()

	// The observation can go stale between the question and the write. Read
	// it again and only then pick. A seat that died in that gap is not
	// written; another eligible seat is, when one remains.
	fresh, err := r.eligibleNow(ctx, workspaceID, settings, candidates)
	if err != nil {
		return Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "routing context incomplete"}, err
	}

	// --- executor slot ---------------------------------------------------
	// A verdict under the threshold is a weak answer, not a missing one: it
	// lands on the ladder's fallback rung — the generic strong seat — instead
	// of leaving the ticket in todo for somebody to notice. A seat the
	// snapshot says cannot take work is not a fallback. The confidence stays
	// visible in the decision comment.
	var executor *Seat
	var notes []string
	executorSource := ""
	if labelled && !labelSeatOK {
		notes = append(notes, "labelled tier "+requestedTier+" was not eligible")
	}
	if needExecutor {
		if len(fresh) == 0 {
			notes = append(notes, "executor not filled: no eligible seat")
		} else {
			labelStill := labelSeatOK && seatIn(fresh, labelSeat)
			seat, source, why := r.pickExecutor(fresh, labelSeat, labelStill, verdict, threshold)
			written, err := r.Store.AssignAgentIfUnassigned(ctx, workspaceID, issue.ID, seat, mode == fillStarts)
			if err != nil {
				return out, err
			}
			if written {
				executor, executorSource = &seat, source
				out.ExecutorWritten = &seat
				if why != "" {
					notes = append(notes, "executor fell back to "+seat.Name+": "+why)
				}
			} else {
				notes = append(notes, "executor not filled: the slot was taken before this call wrote it")
			}
		}
	}

	// --- reviewer slot ---------------------------------------------------
	// Same rule for the second slot: an automatic dispatch fills both. An
	// unusable verdict stays on the executor's rung and picks another model
	// family, or one rung down when that rung has no second family. It never
	// steps up, so the strongest rung appears only when the judge names it
	// with confidence. The slot never names a person: see decideReviewer.
	reviewer := ReviewerRef{}
	reviewerFallback := false
	fallbackWhy := ""
	// The judge asking for a person is kept as a note on the ticket rather
	// than as the slot's value. The seat does the mechanical check and pings
	// the person for the call it cannot make.
	humanSignoff := needReviewer && verdict.Reviewer == ReviewerHuman
	if needReviewer {
		ref, ok := r.decideReviewer(verdict, ladder, direction, roster, fresh, executor, issue)
		why := ""
		switch {
		case !ok && humanSignoff:
			why = "judge asked for a person, and the reviewer slot never names one"
		case !ok:
			why = "judge named tier \"" + verdict.ReviewerTier + "\", which has no seat here"
		case verdict.ReviewerConfidence < threshold:
			ok = false
			why = "confidence " + pct(verdict.ReviewerConfidence) + " < threshold " + pct(threshold)
		}
		if !ok {
			var ladderWhy string
			ref, ladderWhy = r.fallbackReviewer(ladder, direction, roster, fresh, executor, issue)
			reviewerFallback = true
			fallbackWhy = ladderWhy
			notes = append(notes, "reviewer fell back to "+ref.Label()+": "+why)
		}
		if ref.Kind == ReviewerAgent && !seatIn(fresh, Seat{ID: ref.ID}) {
			eligible, err := r.seatStillEligible(ctx, workspaceID, settings, ref.ID)
			if err != nil {
				return out, err
			}
			if !eligible {
				notes = append(notes, "reviewer not filled: seat became ineligible")
				ref = ReviewerRef{}
			}
		}
		if !ref.Empty() {
			written, err := r.Store.SetReviewerIfUnset(ctx, workspaceID, issue.ID, ref)
			if err != nil {
				return out, err
			}
			if written {
				reviewer = ref
				out.ReviewerWritten = ref
			} else {
				notes = append(notes, "reviewer not filled: the slot was taken before this call wrote it")
			}
		}
	}

	// The action reports the writes, not the fact that this row ran. A partial
	// fill is still "assigned", and Reason carries what was not written and
	// every slot that took a fallback.
	if out.ExecutorWritten != nil || !out.ReviewerWritten.Empty() {
		out.Action = ActionAssigned
	}
	out.Reason = strings.Join(notes, "; ")

	// --- notify ----------------------------------------------------------
	// The one condition that earns an @: the ticket is in a state where
	// nobody will move it. Here that means the executor slot is still empty,
	// so the ticket sits in todo until a person notices.
	// A parked sub-issue is the exception: it is not sitting in todo, and its
	// stage's promotion routes it again, so an empty slot there is not yet a
	// ticket nobody will move.
	stillUnassigned := needExecutor && executor == nil
	notify := stillUnassigned && mode != fillParked
	body := r.assignmentComment(issue, match, candidates, verdict, threshold,
		executor, executorSource, reviewer, reviewerFallback, fallbackWhy, humanSignoff,
		needExecutor, needReviewer, notify, mode)
	if note := DemotionFootnote(ladder, roster, executor); note != "" {
		body += "\n\n" + note
	}

	return r.deliver(ctx, workspaceID, issue, KindAssignment, body, notify, out)
}

// Where the executor seat came from, for the decision comment.
const (
	pickLabel    = "label"
	pickJudge    = "judge"
	pickFallback = "fallback"
)

// pickExecutor resolves the executor seat, and always resolves one: candidates
// is non-empty by the time it is called, and every branch ends on a seat. The
// tier label on the ticket wins, then the judge's rung when it is confident
// and exists in this workspace, and the ladder's fallback rung otherwise. The
// third return value is why the fallback happened, empty when it did not.
func (r *Router) pickExecutor(candidates []Seat, labelSeat Seat, labelled bool, v Verdict, threshold float64) (Seat, string, string) {
	if labelled {
		return labelSeat, pickLabel, ""
	}
	seat, ok := SeatByTier(candidates, v.ExecutorTier)
	why := ""
	switch {
	case ok && v.ExecutorConfidence >= threshold:
		return seat, pickJudge, ""
	case ok:
		why = "confidence " + pct(v.ExecutorConfidence) + " < threshold " + pct(threshold)
	default:
		why = "judge named tier \"" + v.ExecutorTier + "\", which has no seat here"
	}
	fallback, fallbackOK := r.Ladder.FallbackSeat(candidates)
	if !fallbackOK {
		// Unreachable while candidates is non-empty; keeping the judge's seat
		// is still better than returning nothing.
		return seat, pickJudge, ""
	}
	return fallback, pickFallback, why
}

// PickAcceptanceSeat chooses a reviewer for an issue that is about to enter
// in_review with an empty reviewer slot. It does not call the judge: the
// rule is the ladder's, same rung and a different model family, or one rung
// down when that rung has only one family. The seat that did the work is
// never returned. ok is false when nothing else can check the work.
func (r *Router) PickAcceptanceSeat(ctx context.Context, workspaceID string, issue Issue) (ReviewerRef, string, bool) {
	if r == nil || r.Store == nil {
		return ReviewerRef{}, "没有验收席名册", false
	}
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return ReviewerRef{}, "读不到工作区的席位设置", false
	}
	ladder := r.Ladder
	if len(ladder.Tiers) == 0 {
		ladder = DefaultLadder
	}
	ladder = ladder.WithProjects(settings.Projects)
	direction := ladder.Direction(issue.ProjectName)
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return ReviewerRef{}, "读不到席位名册", false
	}
	candidates := ladder.Candidates(direction, roster)
	ref, why := r.fallbackReviewer(ladder, direction, roster, candidates, nil, issue)
	if ref.Kind != ReviewerAgent || ref.ID == "" || ref.ID == issue.AssigneeID {
		if why == "" {
			why = "选不出和执行席不同的验收席"
		}
		return ReviewerRef{}, why, false
	}
	return ref, why, true
}

// fallbackReviewer is the reviewer the ladder implies when the judge cannot
// name one with confidence. It never resolves to a person, and it never
// resolves to the strongest rung: that rung is written only when the judge
// names it above the threshold. The check stays on the holder's rung and
// changes model family, or moves one rung down when the rung has no second
// family. 「不需要验收」 is the answer when this workspace has no second seat
// at all. The second return value is why, for the decision comment.
func (r *Router) fallbackReviewer(ladder Ladder, direction string, roster map[string]Agent, candidates []Seat, executor *Seat, issue Issue) (ReviewerRef, string) {
	holder := Seat{}
	switch {
	case executor != nil:
		holder = *executor
	case issue.AssigneeType == "agent" && issue.AssigneeID != "":
		holder = seatFromRoster(ladder, roster, issue.AssigneeID)
	default:
		seat, ok := fallbackNotStrongest(ladder, candidates)
		if !ok {
			return ReviewerRef{Kind: ReviewerNoReview}, "没有不在最强档上的席位能验"
		}
		return seatReviewer(seat), "本票还没有执行席，交兜底档验"
	}
	if holder.TierKey == "" {
		if _, onLadder := SeatIndex(candidates, holder); !onLadder {
			seat, ok := fallbackNotStrongest(ladder, candidates)
			if ok && seat.ID != holder.ID {
				return seatReviewer(seat), "执行席不在档位阶梯上，交兜底档验"
			}
		}
	}
	// The strongest rung is closed on this path even when the holder already
	// sits on it: a same-tier swap would still be strongest, and the judge
	// did not clear that bar.
	if !strings.EqualFold(holder.TierKey, "strongest") {
		if alt, ok := ladder.SameTierAlternate(holder, direction, roster); ok {
			return seatReviewer(alt), "按「同档换一家模型」选的"
		}
	}
	if down, ok := stepDown(candidates, holder.TierKey); ok && down.ID != holder.ID {
		if strings.EqualFold(holder.TierKey, "strongest") {
			return seatReviewer(down), "执行席已在最强档，同档不再派最强档，改由下一档复核"
		}
		return seatReviewer(down), "同档没有第二家模型，改由下一档复核"
	}
	return ReviewerRef{Kind: ReviewerNoReview}, "这个工作区只有一个席位，没有第二个席位能验"
}

// fallbackNotStrongest is the rung an unconfident reviewer may land on when
// there is no holder to stay next to. The declared fallback is used when it
// is not the strongest rung; otherwise the first weaker candidate is.
func fallbackNotStrongest(ladder Ladder, candidates []Seat) (Seat, bool) {
	if seat, ok := ladder.FallbackSeat(candidates); ok && !strings.EqualFold(seat.TierKey, "strongest") {
		return seat, true
	}
	for _, seat := range candidates {
		if !strings.EqualFold(seat.TierKey, "strongest") {
			return seat, true
		}
	}
	return Seat{}, false
}

// stepDown is the candidate one rung below tierKey. Candidates are ordered
// strongest first, one seat per rung. It never walks onto the strongest rung.
func stepDown(candidates []Seat, tierKey string) (Seat, bool) {
	if tierKey == "" {
		return Seat{}, false
	}
	for i, seat := range candidates {
		if !strings.EqualFold(seat.TierKey, tierKey) || i+1 >= len(candidates) {
			continue
		}
		next := candidates[i+1]
		if strings.EqualFold(next.TierKey, "strongest") {
			return Seat{}, false
		}
		return next, true
	}
	return Seat{}, false
}

func seatFromRoster(ladder Ladder, roster map[string]Agent, id string) Seat {
	agent, ok := agentByID(roster, id)
	if !ok {
		return Seat{ID: id}
	}
	seat := Seat{ID: agent.ID, Name: agent.Name, Direction: ladder.seatDirection(agent.Name)}
	key := ""
	if tagged, ok := ladder.NormalizeTier(agent.Tier); ok && tagged != "" {
		key = tagged
	} else if named, ok := ladder.TierOf(agent.Name); ok {
		key = named
	}
	seat.TierKey = key
	if tier, ok := ladder.TierByKey(key); ok {
		seat.TierLabel = tier.Label
	}
	return seat
}

func seatReviewer(s Seat) ReviewerRef {
	return ReviewerRef{Kind: ReviewerAgent, ID: s.ID, Name: s.Name}
}

// decideReviewer turns a verdict branch into the reference to write. A
// reviewer may never be the seat that did the work — checking your own output
// is not a check — so a collision stays on the judged rung and changes model
// family, or moves one rung down when that rung has no second family. It does
// not step up: a higher rung, including the strongest, is written only when
// the judge named that rung.
//
// No branch here can produce a person. A ticket whose reviewer slot names a
// member gets handed to that member at 待验收, and from that moment routing
// never touches it again (an issue a person holds is that person's ticket), so
// the ticket freezes on somebody's desk. "This acceptance needs a person" is
// handled instead by keeping the ticket on a seat and pinging the person —
// see the human-signoff note in the decision comment.
func (r *Router) decideReviewer(v Verdict, ladder Ladder, direction string, roster map[string]Agent, candidates []Seat, executor *Seat, issue Issue) (ReviewerRef, bool) {
	switch v.Reviewer {
	case ReviewerNone:
		return ReviewerRef{Kind: ReviewerNoReview}, true
	case ReviewerHuman:
		// Reported as "not resolvable" so the caller takes the ladder
		// fallback, exactly as it does for any other unusable answer.
		return ReviewerRef{}, false
	case ReviewerSeat:
		seat, ok := SeatByTier(candidates, v.ReviewerTier)
		if !ok {
			return ReviewerRef{}, false
		}
		collides := (executor != nil && seat.ID == executor.ID) ||
			(executor == nil && issue.AssigneeType == "agent" && seat.ID == issue.AssigneeID)
		if collides {
			if alt, ok := ladder.SameTierAlternate(seat, direction, roster); ok {
				seat = alt
			} else if other, ok := stepDown(candidates, seat.TierKey); ok {
				seat = other
			} else {
				return ReviewerRef{}, false
			}
		}
		return seatReviewer(seat), true
	}
	return ReviewerRef{}, false
}

// routeInReview hands the ticket to whoever accepts it. This row does not fill
// a slot, it moves the ticket, so it is not governed by the fill-only rule.
//
// It runs once per stay in review, not once per issue. A handoff comment from
// an earlier stay used to be treated as "the reviewer has already been woken",
// which is how a ticket could enter in_review again — reviewer already
// decided, no run active — and sit there until a person typed an @ (DENE-617,
// DENE-772). The comment is still posted at most once; the wake is not.
//
// A stay that still has a run in flight is left alone. The executor sets
// in_review from inside the run that is about to finish, and starting the
// reviewer in that window races the delivery. The completion callback calls
// Route again once that run is gone.
func (r *Router) routeInReview(ctx context.Context, workspaceID string, settings Settings, issue Issue) (Outcome, error) {
	if issue.ParentIssueID != "" {
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "sub-issue has no acceptance route"}, nil
	}
	out := Outcome{State: StateEnabled, Action: ActionHandedOff}
	decidedHere := false
	if issue.Reviewer.Empty() {
		// Nothing was ever decided for this slot: the ticket was dispatched by
		// hand, or it predates routing. Giving up here is what leaves a queue
		// of tickets sitting in review that nobody was ever told to check, so
		// the reviewer is decided now, by the same judge and the same ladder
		// fallback the todo row uses. This is still a fill, not an overwrite —
		// the write is conditional on the slot being empty.
		ref, outcome, err := r.decideReviewerNow(ctx, workspaceID, settings, issue)
		if err != nil || ref.Empty() {
			return outcome, err
		}
		issue.Reviewer = ref
		out.ReviewerWritten = ref
		decidedHere = true
	}
	if issue.Reviewer.Kind == ReviewerNoReview {
		return Outcome{
			State:           StateEnabled,
			Action:          ActionNoop,
			Reason:          "issue needs no acceptance pass",
			ReviewerWritten: out.ReviewerWritten,
		}, nil
	}

	state, err := r.Store.Acceptance(ctx, workspaceID, issue)
	if err != nil {
		return out, err
	}
	if state.ActiveRun {
		// The run that is still open — almost always the executor finishing
		// the status write — owns the ticket until it ends. Completion calls
		// this row again.
		return Outcome{
			State:           StateEnabled,
			Action:          ActionNoop,
			Reason:          "active run in progress",
			ReviewerWritten: out.ReviewerWritten,
		}, nil
	}

	if issue.Reviewer.Kind == ReviewerMember {
		if state.MemberNotified {
			return Outcome{
				State:           StateEnabled,
				Action:          ActionNoop,
				Reason:          "already notified this round",
				ReviewerWritten: out.ReviewerWritten,
			}, nil
		}
		// A person is named in the slot — by hand, or by a routing version
		// that still wrote people there. The ticket is NOT handed over: an
		// issue a person holds is one routing never touches again, so
		// reassigning here is what leaves the ticket frozen, unable to be
		// moved on by any seat. The person is notified instead; whoever is
		// doing the work keeps the ticket until a seat or a human moves it.
		target := Member{UserID: issue.Reviewer.ID, Name: issue.Reviewer.Name}
		out.Action = ActionAdvised
		out.Reason = "reviewer slot names a person: notified, ticket not reassigned"
		// The inbox row is the notice, and NotifyMember is its only writer.
		// deliverTo would insert another one on a different connection from
		// the stay check, so a status-change hook and the completion callback
		// could each leave a routing_needs_you. The comment is still once per
		// issue; a later stay finds it already posted and only refreshes the
		// notice.
		body := r.reviewerIsPersonComment(issue, target, decidedHere)
		if target.UserID != "" {
			body += "\n\n" + mentionLink(target)
		}
		written, err := r.Store.PostComment(ctx, workspaceID, issue.ID, KindHandoff, body)
		if err != nil {
			return out, err
		}
		if written {
			out.Commented = true
		}
		wrote, err := r.Store.NotifyMember(ctx, workspaceID, issue.ID, target)
		if err != nil {
			return out, err
		}
		if !wrote {
			return Outcome{
				State:           StateEnabled,
				Action:          ActionNoop,
				Reason:          "already notified this round",
				ReviewerWritten: out.ReviewerWritten,
				Commented:       out.Commented,
			}, nil
		}
		out.Mentioned = true
		return out, nil
	}

	if state.AgentEngaged {
		// This stay already put the ticket on the reviewer and started a run.
		// A second status flip, the completion callback, and a manual re-route
		// all land here. The stale sweep is the one path that may wake them
		// again, and it does not come through this check.
		return Outcome{
			State:           StateEnabled,
			Action:          ActionNoop,
			Reason:          "already handed off this round",
			ReviewerWritten: out.ReviewerWritten,
		}, nil
	}

	// An agent reviewer. The reference is resolved against the roster on the
	// spot: a seat that has since been archived is no longer a reviewer, and
	// saying so beats handing the ticket to a seat that cannot run. A seat
	// that is only switched off is still a reviewer — the wake moves to
	// another family instead of vanishing.
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return out, err
	}
	seatAgent, ok := agentByID(roster, issue.Reviewer.ID)
	if !ok {
		card, found, err := r.Store.OffRosterSeat(ctx, workspaceID, issue.Reviewer.ID)
		if err != nil {
			return out, err
		}
		if !found {
			return Outcome{
				State:           StateEnabled,
				Action:          ActionNoop,
				Reason:          "reviewer seat not in roster",
				ReviewerWritten: out.ReviewerWritten,
			}, nil
		}
		return r.handOffToSubstitute(ctx, workspaceID, settings, issue, roster, card, out)
	}
	facts, err := r.Store.RoutingFacts(ctx, workspaceID, []string{seatAgent.ID}, settings.ProviderKeys())
	if err != nil {
		return Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "routing context incomplete"}, err
	}
	if snap, ok := facts.Seats[seatAgent.ID]; ok && Unselectable(snap.Availability) {
		if snap.Availability == AvailabilityDisabled {
			return r.handOffToSubstitute(ctx, workspaceID, settings, issue, roster, seatAgent, out)
		}
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "reviewer seat is not eligible"}, nil
	}
	if err := r.Store.Handoff(ctx, workspaceID, issue.ID, "agent", seatAgent.ID); err != nil {
		return out, err
	}
	// No mention: assignment itself starts the seat's run, so an @ here would
	// only be noise to somebody who is not needed. The comment is once per
	// issue; a later stay still hands off above, it just does not repeat the
	// explanation.
	body := r.handoffComment(issue, seatAgent.Name, decidedHere)
	return r.deliver(ctx, workspaceID, issue, KindHandoff, body, false, out)
}

// handOffToSubstitute covers an acceptance wake whose reviewer cannot take
// work. The slot already names them, so they stay the designated reviewer:
// recovery may give the ticket back only before the cover has started.
func (r *Router) handOffToSubstitute(ctx context.Context, workspaceID string, settings Settings, issue Issue, roster map[string]Agent, disabled Agent, out Outcome) (Outcome, error) {
	ladder := r.Ladder.WithProjects(settings.Projects)
	direction := ladder.Direction(issue.ProjectName)
	holder := seatFromRoster(ladder, map[string]Agent{disabled.Name: disabled}, disabled.ID)
	if holder.Name == "" {
		holder.Name = disabled.Name
	}
	if holder.ID == "" {
		holder.ID = disabled.ID
	}
	var avoid []string
	if issue.AssigneeType == "agent" && issue.AssigneeID != "" && issue.AssigneeID != disabled.ID {
		avoid = append(avoid, issue.AssigneeID)
	}
	replacement, steppedDown, ok := SubstituteSeat(ladder, holder, roster, avoid, direction)
	if !ok {
		body := disabledReviewerStuck(disabled.Name)
		stuck, err := r.deliver(ctx, workspaceID, issue, CommentKind("reviewer_off:"+disabled.ID), body, true, out)
		if err != nil {
			return stuck, err
		}
		stuck.Action = ActionAdvised
		stuck.Reason = "reviewer seat is off and no replacement"
		return stuck, nil
	}
	ref := seatReviewer(replacement)
	written, err := r.Store.ReplaceReviewer(ctx, workspaceID, issue.ID, disabled.ID, ref)
	if err != nil {
		return out, err
	}
	if !written {
		return Outcome{
			State:           StateEnabled,
			Action:          ActionNoop,
			Reason:          "reviewer slot changed",
			ReviewerWritten: out.ReviewerWritten,
		}, nil
	}
	if err := r.Store.RememberReviewerRelay(ctx, workspaceID, issue.ID, ReviewerRelay{
		OriginalID:      disabled.ID,
		OriginalName:    disabled.Name,
		ReplacementID:   replacement.ID,
		ReplacementName: replacement.Name,
		Designated:      true,
		CoveredAt:       time.Now().UTC(),
	}); err != nil {
		return out, err
	}
	issue.Reviewer = ref
	out.ReviewerWritten = ref
	if err := r.Store.Handoff(ctx, workspaceID, issue.ID, "agent", replacement.ID); err != nil {
		return out, err
	}
	note := disabledReviewerNote(disabled.Name, replacement.Name, steppedDown)
	body := note + "\n\n" + r.handoffComment(issue, replacement.Name, false)
	delivered, err := r.deliver(ctx, workspaceID, issue, KindHandoff, body, false, out)
	if err != nil {
		return delivered, err
	}
	if !delivered.Commented {
		return r.deliver(ctx, workspaceID, issue, CommentKind("reviewer_off:"+disabled.ID+":"+replacement.ID), note, false, delivered)
	}
	return delivered, nil
}

// agentByID finds a seat in the roster by id. The roster is keyed by name
// because the ladder addresses seats by name; the reviewer slot addresses
// them by id, which is what survives a rename.
func agentByID(roster map[string]Agent, id string) (Agent, bool) {
	if id == "" {
		return Agent{}, false
	}
	for _, a := range roster {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// decideReviewerNow fills an empty reviewer slot at the in-review row. It is
// the todo row's reviewer half, reached from the other end: same judge, same
// threshold, same ladder fallback, same conditional write. It returns the
// reference that is now in the slot, or an empty reference plus the Outcome to
// return when there is nothing to decide.
func (r *Router) decideReviewerNow(ctx context.Context, workspaceID string, settings Settings, issue Issue) (ReviewerRef, Outcome, error) {
	noop := func(reason string) Outcome {
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: reason}
	}
	ladder := r.Ladder.WithProjects(settings.Projects)
	direction := ladder.Direction(issue.ProjectName)
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return ReviewerRef{}, Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "roster unreadable"}, err
	}
	candidates := ladder.Candidates(direction, roster)
	if len(candidates) == 0 {
		return ReviewerRef{}, noop("ladder has no seat in this workspace"), nil
	}
	eligible, state, err := r.decisionContext(ctx, workspaceID, settings, issue, direction, candidates)
	if err != nil {
		return ReviewerRef{}, Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "routing context incomplete"}, err
	}
	if len(eligible) == 0 {
		return ReviewerRef{}, noop("no eligible seat"), nil
	}

	verdict, err := r.Judge.Assign(ctx, settings.Target(), state)
	if err != nil {
		out, err := r.reportUnavailable(ctx, workspaceID, issue, err)
		return ReviewerRef{}, out, err
	}
	r.Breaker.Succeed(workspaceID)

	fresh, err := r.eligibleNow(ctx, workspaceID, settings, candidates)
	if err != nil {
		return ReviewerRef{}, Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "routing context incomplete"}, err
	}

	// executor is nil on purpose: at this row the ticket is already held by
	// whoever did the work, and decideReviewer reads that holder off the
	// issue to keep a seat from reviewing its own output.
	ref, ok := r.decideReviewer(verdict, ladder, direction, roster, fresh, nil, issue)
	if !ok || verdict.ReviewerConfidence < settings.Threshold() {
		ref, _ = r.fallbackReviewer(ladder, direction, roster, fresh, nil, issue)
	}
	if ref.Kind == ReviewerAgent && !seatIn(fresh, Seat{ID: ref.ID}) {
		eligible, err := r.seatStillEligible(ctx, workspaceID, settings, ref.ID)
		if err != nil {
			return ReviewerRef{}, Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "routing context incomplete"}, err
		}
		if !eligible {
			return ReviewerRef{}, noop("reviewer seat is not eligible"), nil
		}
	}
	if ref.Empty() {
		// Nothing nameable. The slot stays empty rather than holding a
		// reference to nobody.
		return ReviewerRef{}, noop("no reviewer could be resolved"), nil
	}
	written, err := r.Store.SetReviewerIfUnset(ctx, workspaceID, issue.ID, ref)
	if err != nil {
		return ReviewerRef{}, Outcome{State: StateEnabled, Action: ActionHandedOff}, err
	}
	if !written {
		// Another pass filled it between the read and the write. Whatever it
		// wrote is the answer; re-read rather than hand off to a stale one.
		fresh, err := r.Store.Issue(ctx, workspaceID, issue.ID)
		if err != nil {
			return ReviewerRef{}, Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "issue unreadable"}, err
		}
		if fresh.Reviewer.Empty() {
			return ReviewerRef{}, noop("reviewer slot is empty"), nil
		}
		return fresh.Reviewer, Outcome{}, nil
	}
	return ref, Outcome{}, nil
}

// routeBlocked writes nothing. A blocked ticket is stuck on something routing
// cannot know; all it can do is say what it thinks and make sure somebody
// sees it.
func (r *Router) routeBlocked(ctx context.Context, workspaceID string, settings Settings, issue Issue) (Outcome, error) {
	out := Outcome{State: StateEnabled, Action: ActionAdvised}
	done, err := r.Store.HasComment(ctx, workspaceID, issue.ID, KindAdvice)
	if err != nil {
		return out, err
	}
	if done {
		return Outcome{State: StateEnabled, Action: ActionNoop, Reason: "already advised"}, nil
	}

	ladder := r.Ladder.WithProjects(settings.Projects)
	direction := ladder.Direction(issue.ProjectName)
	roster, err := r.Store.Roster(ctx, workspaceID)
	if err != nil {
		return out, err
	}
	candidates := ladder.Candidates(direction, roster)
	_, state, err := r.decisionContext(ctx, workspaceID, settings, issue, direction, candidates)
	if err != nil {
		return Outcome{State: StateEnabled, Action: ActionSkipped, Reason: "routing context incomplete"}, err
	}
	advice, err := r.Judge.Unblock(ctx, settings.Target(), state)
	if err != nil {
		return r.reportUnavailable(ctx, workspaceID, issue, err)
	}
	r.Breaker.Succeed(workspaceID)

	body := r.adviceComment(issue, advice, candidates)
	return r.deliver(ctx, workspaceID, issue, KindAdvice, body, true, out)
}

// reportUnavailable records the failure with the breaker and, on the first
// occurrence for this issue, says so on the ticket and notifies. Once the
// breaker opens, Route returns before reaching here at all, which is what
// keeps a dead model from leaving a trail of identical comments.
func (r *Router) reportUnavailable(ctx context.Context, workspaceID string, issue Issue, cause error) (Outcome, error) {
	r.Breaker.Fail(workspaceID, cause)
	if errors.Is(cause, llm.ErrNotConfigured) {
		// "This deployment never configured an internal LLM" is a fact about
		// the deployment, not about this ticket. Commenting and @-ing on
		// every issue would punish a workspace for an admin's omission, and
		// the spec puts diagnosis in one place: the settings section, which
		// reads this through Health.
		r.log().Warn("routing: internal LLM not configured",
			"workspace_id", workspaceID, "issue_id", issue.ID)
		return Outcome{State: StateIneffective, Action: ActionSkipped, Reason: "internal LLM not configured"}, nil
	}
	r.log().Warn("routing: judge unavailable",
		"workspace_id", workspaceID, "issue_id", issue.ID, "error", cause)
	out := Outcome{State: StateEnabled, Action: ActionUnavailable, Reason: cause.Error()}
	body := r.unavailableComment(issue)
	outcome, err := r.deliver(ctx, workspaceID, issue, KindUnavailable, body, true, out)
	if err != nil {
		return outcome, err
	}
	// The caller sees the judge failure, not a success: a hook that logged
	// "routed" here would hide a broken model behind a green line.
	return outcome, nil
}

// deliver posts a routing comment at most once per issue per kind, and pairs
// every mention with a subscription so the mention actually notifies.
func (r *Router) deliver(ctx context.Context, workspaceID string, issue Issue, kind CommentKind, body string, mention bool, out Outcome) (Outcome, error) {
	already, err := r.Store.HasComment(ctx, workspaceID, issue.ID, kind)
	if err != nil {
		return out, err
	}
	if already {
		return out, nil
	}

	target := Member{}
	if mention {
		target, err = r.Store.NotifyTarget(ctx, workspaceID, issue)
		if err != nil {
			return out, err
		}
		if target.UserID != "" {
			body = body + "\n\n" + mentionLink(target)
		}
	}

	// The comment is written BEFORE anyone is notified, and the notification
	// is conditional on that write having won. HasComment above is a cheap
	// filter, not the guard: two Route calls on the same new issue can both
	// pass it, and only the unique index can decide which one posts. If the
	// loser notified anyway, the person would be pinged about a decision
	// comment that is not theirs and does not exist.
	written, err := r.Store.PostComment(ctx, workspaceID, issue.ID, kind, body)
	if err != nil {
		return out, err
	}
	if !written {
		return out, nil
	}
	out.Commented = true

	if target.UserID != "" {
		if err := r.Store.Subscribe(ctx, workspaceID, issue.ID, target.UserID); err != nil {
			return out, err
		}
		out.Mentioned = true
	}
	return out, nil
}

// deliverTo is deliver with the notification target already decided. The
// in-review handoff to a person mentions THAT person — the one the reviewer
// slot names — not whoever the creator-or-owner rule would resolve today.
func (r *Router) deliverTo(ctx context.Context, workspaceID string, issue Issue, kind CommentKind, body string, target Member, out Outcome) (Outcome, error) {
	already, err := r.Store.HasComment(ctx, workspaceID, issue.ID, kind)
	if err != nil {
		return out, err
	}
	if already {
		return out, nil
	}
	if target.UserID != "" {
		body = body + "\n\n" + mentionLink(target)
	}
	written, err := r.Store.PostComment(ctx, workspaceID, issue.ID, kind, body)
	if err != nil {
		return out, err
	}
	if !written {
		return out, nil
	}
	out.Commented = true
	if target.UserID != "" {
		if err := r.Store.Subscribe(ctx, workspaceID, issue.ID, target.UserID); err != nil {
			return out, err
		}
		out.Mentioned = true
	}
	return out, nil
}

func mentionLink(m Member) string {
	name := m.Name
	if strings.TrimSpace(name) == "" {
		name = "there"
	}
	return "[@" + name + "](mention://member/" + m.UserID + ")"
}

// decisionContext loads the seat and provider summary, drops seats that are
// definitely ineligible, and builds the judge payload. The payload still
// lists the dropped seats so the request shows why a rung is missing.
// candidate_tiers contains only the seats the judge may choose.
func (r *Router) decisionContext(ctx context.Context, workspaceID string, settings Settings, issue Issue, direction string, candidates []Seat) ([]Seat, JudgeState, error) {
	facts, err := r.Store.RoutingFacts(ctx, workspaceID, seatIDs(candidates), settings.ProviderKeys())
	if err != nil {
		return nil, JudgeState{}, err
	}
	eligible := EligibleSeats(candidates, facts.Seats)
	state := r.judgeState(issue, direction, eligible)
	state.RoutingPolicy = DefaultRoutingPolicy
	state.PolicyPrompt = settings.EffectivePolicyPrompt()
	state.Seats = OrderSeatViews(candidates, facts.Seats)
	state.ProviderQuotas = EnsureProviderQuotas(facts.Providers, settings.ProviderKeys())
	return eligible, state, nil
}

// eligibleNow re-reads the summary immediately before a conditional write.
func (r *Router) eligibleNow(ctx context.Context, workspaceID string, settings Settings, candidates []Seat) ([]Seat, error) {
	facts, err := r.Store.RoutingFacts(ctx, workspaceID, seatIDs(candidates), settings.ProviderKeys())
	if err != nil {
		return nil, err
	}
	return EligibleSeats(candidates, facts.Seats), nil
}

func seatIDs(candidates []Seat) []string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.ID != "" {
			ids = append(ids, candidate.ID)
		}
	}
	return ids
}

// seatStillEligible re-reads one seat that was not in the one-per-rung
// candidate list. A same-tier alternate is chosen from the roster, so the
// candidate recheck never saw it. Missing data stays eligible: unknown is
// not a reason to drop the seat.
func (r *Router) seatStillEligible(ctx context.Context, workspaceID string, settings Settings, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	facts, err := r.Store.RoutingFacts(ctx, workspaceID, []string{id}, settings.ProviderKeys())
	if err != nil {
		return false, err
	}
	snap, ok := facts.Seats[id]
	if ok && Unselectable(snap.Availability) {
		return false, nil
	}
	return true, nil
}

func seatIn(seats []Seat, seat Seat) bool {
	if seat.ID == "" {
		return false
	}
	for _, candidate := range seats {
		if candidate.ID == seat.ID {
			return true
		}
	}
	return false
}

func (r *Router) judgeState(issue Issue, direction string, candidates []Seat) JudgeState {
	tiers := make([]string, 0, len(candidates))
	for _, c := range candidates {
		tiers = append(tiers, c.TierKey)
	}
	return JudgeState{
		Title:              issue.Title,
		DescriptionSummary: issue.DescriptionSummary,
		Labels:             issue.Labels,
		Project:            issue.ProjectName,
		Repository:         issue.Repository,
		Status:             issue.Status,
		ParentExecutor:     issue.ParentExecutor,
		HasChildren:        issue.HasChildren,
		Direction:          direction,
		Candidates:         tiers,
	}
}

// ErrNotEnabled is returned by the manual entry point when it is asked to
// route a workspace that has routing switched off, so the CLI can say so
// instead of reporting a silent no-op.
var ErrNotEnabled = errors.New("routing: not enabled for this workspace")

// HealthReport is what the settings section renders. It is assembled from the
// stored settings plus live breaker state, and it is the only surface on which
// a routing failure is visible to a person — tickets stay quiet by design.
type HealthReport struct {
	// State is the same four-way classification the settings section shows.
	State State
	// Usable is false exactly when State is not enabled. The client gates the
	// ineffective chip on this rather than re-deriving it.
	Usable bool
	// Reason is human-readable, and empty unless something is wrong.
	Reason string
	// RetryAfterSeconds is how long the cooldown still has to run.
	RetryAfterSeconds int
	// LastSuccessAt / LastFailureAt are unix seconds, zero for never.
	LastSuccessAt int64
	LastFailureAt int64
	Model         string
	Threshold     float64
	// BaseURL is the workspace's own endpoint, empty when it uses the
	// deployment gateway. Reported raw; the handler reduces it to a host
	// before it reaches a client, because a pasted URL can carry userinfo.
	BaseURL string
	// KeySet reports that a workspace key is stored and openable. It is a
	// boolean and never the key: "is one saved?" is the only question the
	// settings section needs answered, and it is the only one that can be
	// answered without putting a credential on the wire.
	//
	// A stored-but-unopenable key (deployment secret rotated, row restored
	// into a different deployment) reports false, which is what makes the
	// settings section say "type it again" instead of showing a green chip
	// over a key nothing can use.
	KeySet bool
	// UsesWorkspaceGateway is true when this workspace's calls go to its own
	// endpoint rather than the deployment's. Derived from the pair, so a
	// half-filled pair reports false and the section can say why.
	UsesWorkspaceGateway bool
}

// Health answers "is routing actually working for this workspace right now".
//
// It reads; it never dials the model. A settings page that probed on every
// render would turn an open tab into an outbound request loop, and would also
// defeat the breaker it is reporting on. Probe is the explicit re-check.
func (r *Router) Health(ctx context.Context, workspaceID string) (HealthReport, error) {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return HealthReport{}, err
	}
	target := settings.Target()
	out := HealthReport{
		State:                settings.State(),
		Model:                settings.Model,
		Threshold:            settings.Threshold(),
		BaseURL:              target.BaseURL,
		KeySet:               target.APIKey != "",
		UsesWorkspaceGateway: target.Override(),
	}
	h := r.Breaker.Health(workspaceID)
	if !h.LastSuccess.IsZero() {
		out.LastSuccessAt = h.LastSuccess.Unix()
	}
	if !h.LastFailure.IsZero() {
		out.LastFailureAt = h.LastFailure.Unix()
	}
	// Checked before the breaker: a deployment with no internal LLM at all is
	// answerable on the spot, and making the reader wait for a ticket to fail
	// first would leave the settings section green while nothing can work.
	if a, ok := r.Judge.(Availability); ok && out.State == StateEnabled && !a.Available(target) {
		out.State = StateIneffective
		out.Reason = NotConfiguredReason
		out.Usable = false
		return out, nil
	}
	if out.State == StateEnabled && h.Open {
		out.State = StateIneffective
		// Reason is set ONLY here. A failure reason shown next to a healthy
		// chip reads as a current fault; the failure timestamp carries "it
		// stumbled once" without claiming routing is down.
		out.Reason = h.Reason
		out.RetryAfterSeconds = int(h.Retry.Seconds())
	}
	out.Usable = out.State == StateEnabled
	return out, nil
}

// Probe dials the routing model once, on purpose, and folds the answer into
// the breaker. It is what the "re-check" button in the settings section runs:
// the only way out of a cooldown that is shorter than waiting for it.
//
// It asks the judge a fixed, trivial question rather than a second kind of
// request, so a probe that passes is evidence about the call routing actually
// makes.
func (r *Router) Probe(ctx context.Context, workspaceID string) (HealthReport, error) {
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return HealthReport{}, err
	}
	if settings.State() != StateEnabled {
		// Nothing to dial, and dialling anyway would be the one case where
		// a disabled workspace makes an outbound request.
		return r.Health(ctx, workspaceID)
	}
	_, err = r.Judge.Assign(ctx, settings.Target(), JudgeState{
		Title:              "Routing self-check",
		DescriptionSummary: "Connectivity probe issued from the routing settings section. Answer with any tier.",
		Status:             "todo",
		Candidates:         []string{"strong"},
	})
	if err != nil {
		r.Breaker.Fail(workspaceID, err)
	} else {
		r.Breaker.Succeed(workspaceID)
	}
	return r.Health(ctx, workspaceID)
}
