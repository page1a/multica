package stallaction

import "testing"

// The generated fragments must stay byte-identical to the SQL the patrol
// shipped with before DENE-1183, so folding them changed no query.
func TestPatrolSQLMatchesTheShippedPredicates(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{PatrolStatusSQL(), "status IN ('todo','in_progress','blocked')"},
		{QuietSQL(), "last_activity_at < now() - interval '36 hours'"},
		{NotWaitingSQL(), "COALESCE(metadata->>'close.conclusion','') NOT IN ('deferred','continuing') AND COALESCE(metadata->>'block.blocked_by','') = '' AND COALESCE(metadata->>'block.wait_condition','') = ''"},
	} {
		if tc.got != tc.want {
			t.Fatalf("got  %s\nwant %s", tc.got, tc.want)
		}
	}
}

func TestPatrolEligible(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ticket Ticket
		want   bool
	}{
		{"quiet todo", Ticket{Status: "todo"}, true},
		{"blocked without a structured wait", Ticket{Status: "blocked"}, true},
		{"backlog", Ticket{Status: "backlog"}, false},
		{"done", Ticket{Status: "done"}, false},
		{"deferred close", Ticket{Status: "todo", Meta: map[string]any{"close.conclusion": "deferred"}}, false},
		{"continuing close", Ticket{Status: "in_progress", Meta: map[string]any{"close.conclusion": "continuing"}}, false},
		{"delivered close", Ticket{Status: "in_progress", Meta: map[string]any{"close.conclusion": "delivered"}}, true},
		{"blocked by a ticket", Ticket{Status: "blocked", Meta: map[string]any{"block.blocked_by": "DENE-1"}}, false},
		{"waiting on a condition", Ticket{Status: "blocked", Meta: map[string]any{"block.wait_condition": "CI green"}}, false},
	} {
		if got := tc.ticket.PatrolEligible(); got != tc.want {
			t.Fatalf("%s: eligible = %v, want %v", tc.name, got, tc.want)
		}
	}
}
