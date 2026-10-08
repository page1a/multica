package service

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
)

func TestReassignVoidsSeatRunsOnlyForARouterSeatAPersonReplaced(t *testing.T) {
	a := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	b := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	agent := pgtype.Text{String: "agent", Valid: true}
	seat := func(id pgtype.UUID, source string) ExecutorSeat {
		return ExecutorSeat{Type: agent, ID: id, Source: source}
	}

	cases := []struct {
		name       string
		prev, next ExecutorSeat
		want       bool
	}{
		{"router seat replaced by hand", seat(a, routing.SourceRouter), seat(b, routing.SourceHuman), true},
		{"router seat replaced by verified words", seat(a, routing.SourceRouter), seat(b, routing.SourceQuote), true},
		{"router seat re-picked as itself", seat(a, routing.SourceRouter), seat(a, routing.SourceHuman), false},
		{"router seat replaced by routing", seat(a, routing.SourceRouter), seat(b, routing.SourceRouter), false},
		{"router seat replaced by an agent's own pick", seat(a, routing.SourceRouter), seat(b, routing.SourceAgent), false},
		{"a person's seat replaced", seat(a, routing.SourceHuman), seat(b, routing.SourceHuman), false},
		{"a quoted seat replaced", seat(a, routing.SourceQuote), seat(b, routing.SourceHuman), false},
		{"empty slot filled", ExecutorSeat{Source: routing.SourceRouter}, seat(b, routing.SourceHuman), false},
	}
	for _, tc := range cases {
		if got := ReassignVoidsSeatRuns(tc.prev, tc.next); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
