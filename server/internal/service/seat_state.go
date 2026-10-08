package service

import (
	"github.com/multica-ai/multica/server/internal/routing"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// AgentSeatState is an agent row as routing.SeatSelectable reads it: the one
// place a db.Agent becomes "may automatic dispatch pick this seat".
func AgentSeatState(a db.Agent) routing.SeatState {
	return routing.SeatState{
		Archived:     a.ArchivedAt.Valid,
		Disabled:     !a.WorkEnabled,
		NoRuntime:    !a.RuntimeID.Valid,
		DispatchMode: a.DispatchMode,
	}
}
