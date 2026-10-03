package stallaction

import (
	"fmt"
	"slices"
	"strings"

	"github.com/multica-ai/multica/server/internal/blockwait"
	"github.com/multica-ai/multica/server/internal/closeprotocol"
)

// Eligibility is the one reading of status, close.* and block.* that decides
// whether the quiet-ticket patrol may look at a ticket at all (DENE-1183).
// The patrol filters in SQL first and re-checks each row in Go; both sides
// are built from the values below, and a DB-backed parity test pins them to
// the same answer.

// PatrolStatuses are the open statuses the patrol considers. backlog is a
// deliberate park; done and cancelled are terminal.
var PatrolStatuses = []string{"todo", "in_progress", "blocked"}

// WaitKeys are the block-wait fields that say the ticket is waiting on
// something specific (another ticket, a condition) rather than stalled.
var WaitKeys = []string{blockwait.KeyBlockedBy, blockwait.KeyWaitCondition}

// Waiting reports a ticket that explained its stop: a deliberate close
// (deferred / continuing) or a structured wait on a ticket or condition.
func (t Ticket) Waiting() bool {
	if t.Paused() {
		return true
	}
	for _, key := range WaitKeys {
		if blockwait.MetaString(t.Meta, key) != "" {
			return true
		}
	}
	return false
}

// PatrolEligible reports whether the patrol may judge the ticket: an open
// patrol status and no explained stop. Quietness and stall.* progress are
// separate checks, because they need the activity clock.
func (t Ticket) PatrolEligible() bool {
	return slices.Contains(PatrolStatuses, t.Status) && !t.Waiting()
}

// PatrolStatusSQL is PatrolStatuses as a predicate on issue.status.
func PatrolStatusSQL() string { return "status IN (" + sqlList(PatrolStatuses) + ")" }

// QuietSQL is QuietAfter as a predicate on issue.last_activity_at.
func QuietSQL() string {
	return fmt.Sprintf("last_activity_at < now() - interval '%d hours'", int(QuietAfter.Hours()))
}

// NotWaitingSQL is !Waiting as a predicate on issue.metadata.
func NotWaitingSQL() string {
	parts := []string{fmt.Sprintf("COALESCE(metadata->>%s,'') NOT IN (%s)",
		sqlString(closeprotocol.KeyConclusion), sqlList(closeprotocol.ExplainedPauseConclusions))}
	for _, key := range WaitKeys {
		parts = append(parts, fmt.Sprintf("COALESCE(metadata->>%s,'') = ''", sqlString(key)))
	}
	return strings.Join(parts, " AND ")
}

// sqlString quotes a compile-time constant; it is never fed user input.
func sqlString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func sqlList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = sqlString(v)
	}
	return strings.Join(quoted, ",")
}
