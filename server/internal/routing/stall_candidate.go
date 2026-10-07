package routing

import (
	"context"
	"fmt"
	"strings"
)

// StallCandidateState is the bounded context sent to the configured routing
// model when the patrol asks whether a quiet ticket is duplicate or invalid.
// ArtifactCheck is supplied by the handler after it has queried the durable
// delivery tables; the model must account for it in its explanation.
type StallCandidateState struct {
	JudgeState
	QuietHours    int    `json:"quiet_hours"`
	ArtifactCheck string `json:"artifact_check"`
}

type StallCandidateDecision struct {
	Candidate  bool
	Confidence float64
	Reason     string
}

// StallCandidateJudge is optional so deployments whose configured provider
// only speaks the older routing protocol remain safe: they simply leave the
// ticket untouched until an agent submits an explicit review.
type StallCandidateJudge interface {
	Candidate(context.Context, Target, StallCandidateState) (StallCandidateDecision, error)
}

// JudgeStallCandidate runs the configured model for one quiet issue. It never
// writes routing state; the caller owns the announcement transition.
func (r *Router) JudgeStallCandidate(ctx context.Context, workspaceID, issueID, artifactCheck string, quietHours int) (StallCandidateDecision, error) {
	if r == nil || r.Store == nil {
		return StallCandidateDecision{}, fmt.Errorf("stall candidate judge unavailable")
	}
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil || !settings.State().Active() {
		return StallCandidateDecision{}, fmt.Errorf("stall candidate judge unavailable")
	}
	if open, _, _ := r.Breaker.Open(workspaceID); open {
		return StallCandidateDecision{}, fmt.Errorf("stall candidate judge cooling down")
	}
	issue, err := r.Store.Issue(ctx, workspaceID, issueID)
	if err != nil {
		return StallCandidateDecision{}, err
	}
	judge, ok := r.Judge.(StallCandidateJudge)
	if !ok {
		return StallCandidateDecision{}, fmt.Errorf("configured routing model does not support stall candidates")
	}
	target := settings.Target()
	if settings.AnalysisOn() && !settings.JudgeOn() {
		target = settings.AnalysisTarget()
	}
	return judge.Candidate(ctx, target, StallCandidateState{
		JudgeState: r.judgeState(issue, GenericScene, nil), QuietHours: quietHours,
		ArtifactCheck: strings.TrimSpace(artifactCheck),
	})
}
