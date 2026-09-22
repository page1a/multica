package scheduler

import (
	"context"
	"time"

	"github.com/multica-ai/multica/server/internal/routing"
)

// JobNameRoutingStaleReview is the canonical job name written to
// sys_cron_executions audit rows. Stable across releases — renaming it would
// orphan historic rows.
const JobNameRoutingStaleReview = "routing_stale_review"

// RoutingSweeper is the narrow contract this job needs from the router.
// Declared here so the scheduler's own tests can stub it.
type RoutingSweeper interface {
	Sweep(ctx context.Context) (routing.SweepReport, error)
}

// RoutingStaleReviewJob returns the JobSpec that drives the routing module's
// stale-review sweep (DENE-712 B2).
//
// Why the scheduler and not an Autopilot. Every other routing row hangs off an
// event the product already delivers — an issue was created, a status changed
// — and a stall is the absence of events, so it needs a clock. The two
// candidates were an Autopilot and this. The sweep is server-internal
// bookkeeping that must run for every workspace with routing on, which an
// Autopilot cannot promise: it is a per-workspace object somebody has to
// create and keep enabled, it routes through the agent task queue to do work
// that never leaves the server, and a workspace that turned routing on would
// silently not get the behaviour until somebody also built the Autopilot. This
// scheduler already gives the three things a cross-workspace periodic job
// needs and nothing else here provides: a distributed lease so N replicas run
// it once, an audit row per run, and retry with stale-lease recovery.
//
// Timing. The cadence is deliberately much shorter than the stall threshold
// (24h by default): the cadence decides how late a sweep notices a stall, not
// how long a ticket must be quiet, and the sweep is idempotent — a ticket
// already woken stays quiet until it is woken again, and the one-comment-per
// -kind index caps what a person can be told at once, whatever the cadence.
//
// CatchUpLatestOnly for the same reason: the sweep asks "what is stale NOW".
// Replaying the buckets a restarted server missed would re-ask the same
// question of the same tickets, which is work without an answer that differs.
func RoutingStaleReviewJob(sweeper RoutingSweeper) JobSpec {
	return JobSpec{
		Name:              JobNameRoutingStaleReview,
		Cadence:           30 * time.Minute,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     6 * time.Hour,
		RunTimeout:        10 * time.Minute,
		StaleTimeout:      15 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       2,
		RetryBackoff:      []time.Duration{5 * time.Minute},
		Scopes:            StaticScopes(ScopeGlobal),
		Handler:           makeRoutingStaleReviewHandler(sweeper),
	}
}

func makeRoutingStaleReviewHandler(sweeper RoutingSweeper) Handler {
	return func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
		if sweeper == nil {
			// Routing is not wired in this deployment. A registered job with
			// nothing behind it should record an empty run, not an error.
			return HandlerResult{}, nil
		}
		report, err := sweeper.Sweep(ctx)
		if in.Heartbeat != nil {
			_ = in.Heartbeat(ctx)
		}
		result := map[string]any{
			"workspaces": report.Workspaces,
			"examined":   report.Examined,
			"woken":      report.Woken,
			"completed":  report.Completed,
			"failed":     report.Failed,
		}
		if err != nil {
			return HandlerResult{Result: result}, err
		}
		// rows_affected counts what the sweep actually changed, so an audit
		// row of zero is the normal quiet case and stands out from a run that
		// moved tickets.
		return HandlerResult{
			RowsAffected: int64(report.Woken + report.Completed),
			Result:       result,
		}, nil
	}
}
