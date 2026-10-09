package service

import (
	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
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
		Projects:     AgentDispatchProjects(a),
	}
}

// AgentDispatchProjects is the agent's project limit as ids; empty when the
// seat serves every project.
func AgentDispatchProjects(a db.Agent) []string {
	if len(a.DispatchProjects) == 0 {
		return nil
	}
	out := make([]string, 0, len(a.DispatchProjects))
	for _, id := range a.DispatchProjects {
		if id.Valid {
			out = append(out, util.UUIDToString(id))
		}
	}
	return out
}
