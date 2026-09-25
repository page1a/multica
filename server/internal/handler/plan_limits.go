package handler

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	maxPlanLimitWindows       = 8
	maxPlanLimitWindowMinutes = 365 * 24 * 60
)

func validatePlanLimitsSnapshot(snapshot *protocol.PlanLimitsSnapshot, runtimeProvider string) ([]byte, error) {
	if snapshot == nil {
		return nil, nil
	}
	if snapshot.Provider != runtimeProvider {
		return nil, fmt.Errorf("provider does not match runtime")
	}
	if snapshot.Status != protocol.PlanLimitsStatusAvailable && snapshot.Status != protocol.PlanLimitsStatusExhausted {
		return nil, fmt.Errorf("unsupported status")
	}
	if snapshot.ObservedAt <= 0 {
		return nil, fmt.Errorf("observed_at must be positive")
	}
	if len(snapshot.Windows) > maxPlanLimitWindows {
		return nil, fmt.Errorf("too many windows")
	}
	if snapshot.Status == protocol.PlanLimitsStatusAvailable && len(snapshot.Windows) == 0 {
		return nil, fmt.Errorf("available snapshot requires a window")
	}

	seen := make(map[string]struct{}, len(snapshot.Windows))
	for _, window := range snapshot.Windows {
		if !validPlanLimitWindowName(window.Name) {
			return nil, fmt.Errorf("invalid window name")
		}
		if _, exists := seen[window.Name]; exists {
			return nil, fmt.Errorf("duplicate window name")
		}
		seen[window.Name] = struct{}{}
		if window.UsedPercent == nil && window.ResetsAt == nil && window.Remaining == nil {
			return nil, fmt.Errorf("window requires usage or reset data")
		}
		if window.UsedPercent != nil && (math.IsNaN(*window.UsedPercent) || math.IsInf(*window.UsedPercent, 0) || *window.UsedPercent < 0 || *window.UsedPercent > 100) {
			return nil, fmt.Errorf("used_percent must be between 0 and 100")
		}
		if window.Remaining != nil && (math.IsNaN(*window.Remaining) || math.IsInf(*window.Remaining, 0) || *window.Remaining < 0) {
			return nil, fmt.Errorf("remaining must be non-negative")
		}
		if window.WindowMinutes != nil && (*window.WindowMinutes <= 0 || *window.WindowMinutes > maxPlanLimitWindowMinutes) {
			return nil, fmt.Errorf("window_minutes is out of range")
		}
		if window.ResetsAt != nil && *window.ResetsAt <= 0 {
			return nil, fmt.Errorf("resets_at must be positive")
		}
	}

	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal plan limits: %w", err)
	}
	return data, nil
}

// maxAgentPlanLimitsPerHeartbeat caps how many per-agent snapshots one
// heartbeat may carry, so a runaway daemon cannot turn its liveness signal into
// an unbounded write burst. A machine's account-bound set is a handful of seats.
const maxAgentPlanLimitsPerHeartbeat = 64

// validateAgentPlanLimits normalizes the per-agent snapshots of one heartbeat
// and keys them by the parsed agent id (DENE-715).
//
// Unlike the runtime snapshot, a bad entry here never fails the heartbeat: the
// heartbeat is also the daemon's liveness signal, and one unusable seat is not
// worth taking every runtime on that machine offline. Invalid entries are
// dropped and named in the returned error for the caller to log.
func validateAgentPlanLimits(snapshots map[string]protocol.PlanLimitsSnapshot, runtimeProvider string) (map[pgtype.UUID][]byte, error) {
	if len(snapshots) == 0 {
		return nil, nil
	}
	if len(snapshots) > maxAgentPlanLimitsPerHeartbeat {
		return nil, fmt.Errorf("too many agent plan limits")
	}

	out := make(map[pgtype.UUID][]byte, len(snapshots))
	var dropped []string
	for rawID, snapshot := range snapshots {
		agentID := strings.TrimSpace(rawID)
		uuid, err := util.ParseUUID(agentID)
		if err != nil {
			dropped = append(dropped, "invalid agent id")
			continue
		}
		entry := snapshot
		data, err := validatePlanLimitsSnapshot(&entry, runtimeProvider)
		if err != nil || len(data) == 0 {
			dropped = append(dropped, agentID)
			continue
		}
		out[uuid] = data
	}
	if len(dropped) > 0 {
		sort.Strings(dropped)
		return out, fmt.Errorf("dropped %d agent plan limits: %s", len(dropped), strings.Join(dropped, ", "))
	}
	return out, nil
}

func validPlanLimitWindowName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if name != trimmed || name == "" || len(name) > 32 {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}
