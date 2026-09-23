package routing

import (
	"context"
	"time"
)

// Issue is the slice of an issue routing reads. Everything here is either fed
// to the judge in trimmed form or used by the state table; nothing else about
// an issue reaches this package.
type Issue struct {
	ID    string
	Title string
	// DescriptionSummary is a clipped description. The full body never leaves
	// the deployment.
	DescriptionSummary string
	// Status is the status CATEGORY (todo / in_progress / in_review / done /
	// blocked / backlog / cancelled), already resolved from any custom status.
	// The state table is written against the seven categories, so a workspace
	// that renamed its statuses routes identically to one that did not.
	Status string

	AssigneeType string // "", "member", "agent", "squad"
	AssigneeID   string
	CreatorType  string // "member" or "agent"
	CreatorID    string

	ProjectID   string
	ProjectName string
	Repository  string
	Labels      []string
	// ParentIssueID is empty for a top-level issue. Child issues still use
	// routing for executor dispatch, but acceptance belongs to their parent
	// and must never be routed independently.
	ParentIssueID string

	ParentExecutor string
	HasChildren    bool

	// Reviewer is the ticket's reviewer slot. A zero Kind means the slot is
	// empty — the only condition under which routing may write it.
	Reviewer ReviewerRef

	// LastActivityAt is when anything last happened on this ticket. The
	// stale-review row measures quiet from here; every other row ignores it.
	// Zero means the store could not tell, which that row reads as "not
	// stale" rather than as "stale forever".
	LastActivityAt time.Time
}

// ReviewerTarget is what the reviewer slot holds. The values are the strings
// stored in issue.reviewer_type, so the module and the column agree by
// construction.
type ReviewerTarget string

const (
	// ReviewerEmpty is the unfilled slot.
	ReviewerEmpty ReviewerTarget = ""
	// ReviewerAgent — a seat accepts the ticket. ID is the agent id.
	ReviewerAgent ReviewerTarget = "agent"
	// ReviewerMember — a person accepts the ticket. ID is the user id.
	//
	// Routing never WRITES this value; only a person filling the slot by hand
	// does, and tickets routed by earlier versions still carry it. At 待验收
	// it means "ping this person", not "hand them the ticket": handing it over
	// puts the ticket beyond routing's reach for good, which is what left
	// people unable to move its status.
	ReviewerMember ReviewerTarget = "member"
	// ReviewerNoReview — this ticket needs no acceptance pass. It is a
	// WRITTEN value and carries no id: an empty slot would be re-judged on
	// every later status change, and the fill-only-empty-slots rule would
	// never close.
	ReviewerNoReview ReviewerTarget = "none"
)

// ReviewerRef is the reviewer slot's value: a reference to an actor, the same
// shape as the assignee pair. Name is display only — it is resolved from the
// roster on read and never stored, which is the whole point of the reference:
// renaming or archiving a seat cannot leave a ticket pointing at a name that
// no longer means anybody.
type ReviewerRef struct {
	Kind ReviewerTarget
	ID   string
	Name string
}

// Empty reports the one state routing may write over.
func (r ReviewerRef) Empty() bool { return r.Kind == ReviewerEmpty }

// Label is what the decision comment prints for this slot.
func (r ReviewerRef) Label() string {
	switch r.Kind {
	case ReviewerNoReview:
		return LabelNoReview
	case ReviewerMember:
		if r.Name != "" {
			return r.Name
		}
		return LabelHuman
	case ReviewerAgent:
		return r.Name
	}
	return ""
}

// AssignedToHuman reports the one case routing never touches at all. A ticket
// a person is holding is that person's ticket: no slot is filled, no comment
// is posted, and nobody is notified. This is the single exception that sits
// above the fill-only-empty-slots rule.
func (i Issue) AssignedToHuman() bool { return i.AssigneeType == "member" }

// Member is a notification target.
type Member struct {
	UserID string
	Name   string
}

// Labels for the two non-seat answers, used in routing comments only.
const (
	LabelNoReview = "不需要验收"
	LabelHuman    = "交给人"
)

// CommentKind identifies a routing comment. It is also the de-duplication key:
// one comment of each kind per issue, which is what keeps repeated status
// flips from re-notifying and re-explaining.
type CommentKind string

const (
	// KindAssignment — the todo-row decision comment.
	KindAssignment CommentKind = "assignment"
	// KindHandoff — the in-review-row handoff comment.
	KindHandoff CommentKind = "handoff"
	// KindAdvice — the blocked-row advice comment. Writes no values.
	KindAdvice CommentKind = "advice"
	// KindUnavailable — the model could not be reached on a workspace that is
	// configured. Posted once, then the breaker keeps the issue quiet.
	KindUnavailable CommentKind = "unavailable"
	// KindStalled — the stale-review row could not wake anybody by writing a
	// value, because the acceptance belongs to a PERSON. Posted once per
	// issue and paired with an @, which is the only thing that reaches them.
	//
	// The agent half of the same row needs no comment: reassigning a seat
	// starts its run, and that reassignment is already in the timeline. A
	// comment per sweep is impossible anyway — one comment of each kind per
	// issue is the de-duplication key — and it is the right impossibility:
	// the wake may repeat, the noise may not.
	KindStalled CommentKind = "stalled"
	// KindCompleted — the stale-review row aligned the status to an
	// acceptance the ticket already carried. At most once per issue by
	// nature: there is only one such transition.
	KindCompleted CommentKind = "completed"
)

// Store is everything Route needs from the rest of the server. Every write on
// it is either conditional (the ...IfUnset pair) or explicitly a handoff, so
// the fill-only-empty-slots rule is enforced in SQL rather than by reading
// first and writing after — two nearly simultaneous Route calls on the same
// new issue would both read an empty slot and both write it.
type Store interface {
	Settings(ctx context.Context, workspaceID string) (Settings, error)
	Issue(ctx context.Context, workspaceID, issueID string) (Issue, error)
	// Roster maps agent name to agent for the whole workspace.
	Roster(ctx context.Context, workspaceID string) (map[string]Agent, error)
	// RoutingFacts is the seat and provider summary for one decision. agentIDs
	// are the candidates; providers are the quota subscription. A store error
	// is an incomplete context: the caller writes nothing. A missing
	// observation is not an error — it comes back as unknown.
	RoutingFacts(ctx context.Context, workspaceID string, agentIDs, providers []string) (RoutingFacts, error)
	// AssignAgentIfUnassigned fills the executor slot only while it is still
	// empty. Reports whether THIS call wrote it. Starting the seat's run is
	// the store's job, because assignment is what wakes an agent.
	AssignAgentIfUnassigned(ctx context.Context, workspaceID, issueID string, seat Seat) (written bool, err error)
	// SetReviewerIfUnset fills the reviewer slot only while it is still empty.
	// The slot is a field on the issue, not a workspace property, so there is
	// nothing to provision and nothing that can be missing: every workspace
	// with routing on has it.
	SetReviewerIfUnset(ctx context.Context, workspaceID, issueID string, ref ReviewerRef) (written bool, err error)
	// Handoff reassigns an issue that already has an assignee. Unlike the two
	// above this is not a fill: the in-review row hands the ticket from the
	// seat that did the work to the seat that accepts it. It is only ever
	// called with assigneeType "agent" — routing does not hand tickets to
	// people, it notifies them.
	Handoff(ctx context.Context, workspaceID, issueID, assigneeType, assigneeID string) error

	HasComment(ctx context.Context, workspaceID, issueID string, kind CommentKind) (bool, error)
	// PostComment writes one routing comment and reports whether THIS call
	// wrote it. False means a concurrent Route call got there first and the
	// database rejected the duplicate — the caller must then not perform the
	// notification work that belongs to the comment it did not post.
	PostComment(ctx context.Context, workspaceID, issueID string, kind CommentKind, body string) (written bool, err error)
	// Subscribe adds a user to the issue's subscribers. A mention alone very
	// often does not notify; notification follows subscription, so every @
	// this package writes is paired with one of these.
	Subscribe(ctx context.Context, workspaceID, issueID, userID string) error
	// NotifyTarget resolves who to @ for this issue: the creator, or the
	// workspace owner when the creator is an agent. One rule, no setting.
	NotifyTarget(ctx context.Context, workspaceID string, issue Issue) (Member, error)

	// --- the stale-review row -------------------------------------------
	//
	// These four exist for one row of the table and are used nowhere else.

	// EnabledWorkspaces lists the workspaces whose routing switch is on. The
	// sweep has no request to hang off, so it has to find its own work; every
	// workspace it returns is re-checked against Settings anyway, because the
	// switch can flip between this call and the pass.
	EnabledWorkspaces(ctx context.Context) ([]string, error)

	// StaleReviews lists issues in this workspace that sit in the in_review
	// CATEGORY, carry no active run, and have had no activity since `before`.
	//
	// "No activity" rather than "entered review at": a ticket somebody
	// commented on an hour ago is not stalled, whenever it entered review.
	// Quiet is the thing this row is about.
	StaleReviews(ctx context.Context, workspaceID string, before time.Time, limit int) ([]string, error)

	// ReviewRemarks returns what the ticket's reviewer has said on it IN THE
	// CURRENT review round — since the ticket last entered the in_review
	// category — oldest first. An empty result is the deterministic half of
	// the completion gate: with nothing from the reviewer in this round there
	// is no acceptance to align to, whatever the judge answers.
	//
	// The round boundary is part of the contract, not an implementation
	// detail. A pass verdict from an earlier round, before the ticket was
	// sent back and redone, is an expired fact, and aligning a status to an
	// expired fact is the one thing this row must never do. A store that
	// cannot establish where the round began returns nothing, which routes
	// to the wake.
	ReviewRemarks(ctx context.Context, workspaceID, issueID string, reviewer ReviewerRef) ([]string, error)

	// CompleteFromReview is the ONLY status write this package has, and it is
	// conditional: it moves the ticket to done only while it is still in the
	// in_review category, and reports whether THIS call wrote it. A ticket
	// that moved between the decision and the write is left alone.
	CompleteFromReview(ctx context.Context, workspaceID, issueID string) (written bool, err error)
}
