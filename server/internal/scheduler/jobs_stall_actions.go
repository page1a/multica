package scheduler

import (
	"context"
	"time"
)

const JobNameIssueStallActions = "issue_stall_actions"

// StallActionSweeper is intentionally tiny so the scheduler does not depend
// on the HTTP handler package. The handler owns the shared state transitions;
// this job only supplies the clock and distributed lease.
type StallActionSweeper interface {
	SweepStallActions(context.Context) (int64, error)
}

func IssueStallActionsJob(sweeper StallActionSweeper) JobSpec {
	return JobSpec{
		Name: JobNameIssueStallActions, Cadence: 30 * time.Minute,
		CatchUpMode: CatchUpLatestOnly, CatchUpWindow: 6 * time.Hour,
		RunTimeout: 10 * time.Minute, StaleTimeout: 15 * time.Minute,
		HeartbeatInterval: 30 * time.Second, AllowStaleReentry: true,
		MaxAttempts: 2, RetryBackoff: []time.Duration{5 * time.Minute},
		Scopes: StaticScopes(ScopeGlobal),
		Handler: func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
			if sweeper == nil {
				return HandlerResult{}, nil
			}
			n, err := sweeper.SweepStallActions(ctx)
			if in.Heartbeat != nil {
				_ = in.Heartbeat(ctx)
			}
			return HandlerResult{RowsAffected: n, Result: map[string]any{"actions": n}}, err
		},
	}
}
