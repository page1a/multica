package routing

import (
	"context"
	"errors"
	"sync"
	"time"
)

// fakeStore records every write so a test can assert not just the result but
// that nothing else was touched — most of this package's rules are about what
// it must NOT do.
type fakeStore struct {
	mu sync.Mutex

	settings Settings
	issue    Issue
	roster   map[string]Agent
	facts    RoutingFacts
	// factsAfter, when set, is what the second RoutingFacts read returns.
	// The first read is the one the judge sees; the second is the recheck
	// immediately before a write.
	factsAfter *RoutingFacts
	factReads  int
	target     Member

	// slot occupancy, as the database would enforce it
	assigneeTaken bool
	reviewerTaken bool

	// stale-review row
	workspaces    []string
	staleIDs      []string
	remarks       []string
	statusWritten []string
	completeLost  bool

	comments map[CommentKind][]string
	subs     []string
	handoffs []string
	assigns  []string
	// quietAssigns are the assigns written without starting a run.
	quietAssigns []string
	reviewer     []string
	// offRoster is seats Roster hides because work is switched off.
	offRoster map[string]Agent
	relays    []ReviewerRelay

	// activeRun is the executor's still-open task. The in-review row must
	// not start the reviewer until it is cleared, which is what the
	// completion callback does.
	activeRun bool
	// reviewerRuns records seats that already have a run for this stay.
	// Handoff sets it, the same way the real store reads the task row.
	reviewerRuns map[string]bool
	// memberNotified records people already sent this stay's notice.
	memberNotified map[string]bool

	errOn map[string]error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		settings: Settings{Enabled: true, Model: "test-model", ConfidenceThreshold: 0.7},
		issue: Issue{
			ID:          "issue-1",
			Title:       "做一件事",
			Status:      "todo",
			CreatorType: "member",
			CreatorID:   "user-1",
			ProjectName: "game",
		},
		roster: map[string]Agent{
			"布尔玛":   {ID: "a-bulma", Name: "布尔玛"},
			"孙悟空":   {ID: "a-goku", Name: "孙悟空"},
			"贝吉塔":   {ID: "a-vegeta", Name: "贝吉塔"},
			"比克":    {ID: "a-piccolo", Name: "比克"},
			"布尔玛游戏": {ID: "a-bulma-g", Name: "布尔玛游戏"},
			"孙悟空游戏": {ID: "a-goku-g", Name: "孙悟空游戏"},
			"贝吉塔游戏": {ID: "a-vegeta-g", Name: "贝吉塔游戏"},
			"比克游戏":  {ID: "a-piccolo-g", Name: "比克游戏"},
		},
		target:         Member{UserID: "user-1", Name: "Kun"},
		comments:       map[CommentKind][]string{},
		reviewerRuns:   map[string]bool{},
		memberNotified: map[string]bool{},
		errOn:          map[string]error{},
	}
}

func (f *fakeStore) fail(op string) error { return f.errOn[op] }

func (f *fakeStore) Settings(context.Context, string) (Settings, error) {
	return f.settings, f.fail("settings")
}
func (f *fakeStore) Issue(context.Context, string, string) (Issue, error) {
	return f.issue, f.fail("issue")
}
func (f *fakeStore) Roster(context.Context, string) (map[string]Agent, error) {
	return f.roster, f.fail("roster")
}
func (f *fakeStore) RoutingFacts(context.Context, string, []string, []string) (RoutingFacts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.factReads++
	if err := f.fail("facts"); err != nil {
		return RoutingFacts{}, err
	}
	if f.factReads > 1 && f.factsAfter != nil {
		return *f.factsAfter, nil
	}
	return f.facts, nil
}
func (f *fakeStore) AssignAgentIfUnassigned(_ context.Context, _, _ string, seat Seat, start bool) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("assign"); err != nil {
		return false, err
	}
	if f.assigneeTaken || f.issue.AssigneeType != "" {
		return false, nil
	}
	f.assigneeTaken = true
	f.assigns = append(f.assigns, seat.Name)
	if !start {
		f.quietAssigns = append(f.quietAssigns, seat.Name)
	}
	return true, nil
}

func (f *fakeStore) SetReviewerIfUnset(_ context.Context, _, _ string, ref ReviewerRef) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("set_reviewer"); err != nil {
		return false, err
	}
	if f.reviewerTaken || !f.issue.Reviewer.Empty() {
		return false, nil
	}
	f.reviewerTaken = true
	// Records the LABEL, not the id: every assertion in this package is about
	// who was chosen, and an id would make each one restate the fixture.
	f.reviewer = append(f.reviewer, ref.Label())
	return true, nil
}

func (f *fakeStore) OffRosterSeat(_ context.Context, _, agentID string) (Agent, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("off_roster"); err != nil {
		return Agent{}, false, err
	}
	agent, ok := f.offRoster[agentID]
	return agent, ok, nil
}

func (f *fakeStore) ReplaceReviewer(_ context.Context, _, _, currentID string, ref ReviewerRef) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("replace_reviewer"); err != nil {
		return false, err
	}
	if f.issue.Reviewer.Kind != ReviewerAgent || f.issue.Reviewer.ID != currentID {
		return false, nil
	}
	f.issue.Reviewer = ref
	f.reviewerTaken = true
	f.reviewer = append(f.reviewer, ref.Label())
	return true, nil
}

func (f *fakeStore) RememberReviewerRelay(_ context.Context, _, _ string, note ReviewerRelay) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("remember_relay"); err != nil {
		return err
	}
	f.relays = append(f.relays, note)
	return nil
}

func (f *fakeStore) Handoff(_ context.Context, _, _, assigneeType, assigneeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("handoff"); err != nil {
		return err
	}
	f.handoffs = append(f.handoffs, assigneeType+":"+assigneeID)
	// The real handoff both moves the ticket and starts the run. Recording
	// both is what lets a second Route see "already handed off this round"
	// instead of handing off again.
	f.issue.AssigneeType = assigneeType
	f.issue.AssigneeID = assigneeID
	if assigneeType == "agent" {
		f.reviewerRuns[assigneeID] = true
	}
	return nil
}

func (f *fakeStore) Acceptance(_ context.Context, _ string, issue Issue) (AcceptanceState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("acceptance"); err != nil {
		return AcceptanceState{}, err
	}
	engaged := issue.Reviewer.Kind == ReviewerAgent &&
		issue.AssigneeType == "agent" &&
		issue.AssigneeID == issue.Reviewer.ID &&
		f.reviewerRuns[issue.Reviewer.ID]
	return AcceptanceState{
		ActiveRun:      f.activeRun,
		AgentEngaged:   engaged,
		MemberNotified: f.memberNotified[issue.Reviewer.ID],
	}, nil
}

func (f *fakeStore) NotifyMember(_ context.Context, _, _ string, member Member) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("notify_member"); err != nil {
		return false, err
	}
	if member.UserID == "" || f.memberNotified[member.UserID] {
		return false, nil
	}
	f.memberNotified[member.UserID] = true
	f.subs = append(f.subs, member.UserID)
	return true, nil
}

func (f *fakeStore) HasComment(_ context.Context, _, _ string, kind CommentKind) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.comments[kind]) > 0, f.fail("has_comment")
}

func (f *fakeStore) PostComment(_ context.Context, _, _ string, kind CommentKind, body string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("post_comment"); err != nil {
		return false, err
	}
	// Mirrors the unique index: one comment of each kind per issue, and the
	// second writer is told it did not write.
	if len(f.comments[kind]) > 0 {
		return false, nil
	}
	f.comments[kind] = append(f.comments[kind], body)
	return true, nil
}

func (f *fakeStore) Subscribe(_ context.Context, _, _, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("subscribe"); err != nil {
		return err
	}
	f.subs = append(f.subs, userID)
	f.memberNotified[userID] = true
	return nil
}

func (f *fakeStore) EnabledWorkspaces(context.Context) ([]string, error) {
	return f.workspaces, f.fail("enabled_workspaces")
}

func (f *fakeStore) StaleReviews(_ context.Context, _ string, _ time.Time, _ int) ([]string, error) {
	return f.staleIDs, f.fail("stale_reviews")
}

func (f *fakeStore) ReviewRemarks(_ context.Context, _, _ string, _ ReviewerRef) ([]string, error) {
	return f.remarks, f.fail("review_remarks")
}

func (f *fakeStore) CompleteFromReview(_ context.Context, _, issueID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("complete"); err != nil {
		return false, err
	}
	// The conditional write losing its race: the ticket left in_review between
	// the decision and the write.
	if f.completeLost {
		return false, nil
	}
	f.statusWritten = append(f.statusWritten, issueID)
	return true, nil
}

func (f *fakeStore) NotifyTarget(context.Context, string, Issue) (Member, error) {
	return f.target, f.fail("notify_target")
}

// wrote reports whether this store saw any value write at all. Several rules
// are stated as "does not change any value", and this is how they are checked.
func (f *fakeStore) wrote() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.assigns) > 0 || len(f.reviewer) > 0 || len(f.handoffs) > 0 ||
		len(f.statusWritten) > 0
}

func (f *fakeStore) commentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.comments {
		n += len(v)
	}
	return n
}

// fakeJudge answers whatever the test tells it to, and counts calls so tests
// can prove no request was made.
type fakeJudge struct {
	mu         sync.Mutex
	verdict    Verdict
	advice     Advice
	stale      StaleDecision
	err        error
	calls      int
	lastAssign JudgeState
}

func (j *fakeJudge) Assign(_ context.Context, _ Target, st JudgeState) (Verdict, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.calls++
	j.lastAssign = st
	return j.verdict, j.err
}

func (j *fakeJudge) Unblock(context.Context, Target, JudgeState) (Advice, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.calls++
	return j.advice, j.err
}

func (j *fakeJudge) Stale(context.Context, Target, StaleState) (StaleDecision, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.calls++
	return j.stale, j.err
}

func (j *fakeJudge) callCount() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.calls
}

func (j *fakeJudge) assignedState() JudgeState {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lastAssign
}

func confidentVerdict() Verdict {
	return Verdict{
		ExecutorTier: "strong", ExecutorConfidence: 0.9,
		Reviewer: ReviewerSeat, ReviewerTier: "strongest", ReviewerConfidence: 0.9,
		Reason: "中等复杂度",
	}
}

var errUpstream = errors.New("upstream exploded")

func newRouter(store Store, judge Judge) *Router {
	r := New(store, judge)
	return r
}
