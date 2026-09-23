package handler

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/routing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// RoutingFacts is the seat and provider summary Route reads before it asks
// the judge, and again immediately before it writes. Provider quota and seat
// liveness stay separate: a missing Claude reading does not mark every Claude
// seat dead, and a dead runtime does not invent a quota number.
func (s routingStore) RoutingFacts(ctx context.Context, workspaceID string, agentIDs, providers []string) (routing.RoutingFacts, error) {
	wsID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return routing.RoutingFacts{}, err
	}
	now := time.Now()
	runtimes, err := s.h.Queries.ListAgentRuntimes(ctx, wsID)
	if err != nil {
		return routing.RoutingFacts{}, err
	}
	byRuntime := make(map[string]db.AgentRuntime, len(runtimes))
	for _, runtime := range runtimes {
		byRuntime[util.UUIDToString(runtime.ID)] = runtime
	}

	seats := map[string]routing.SeatSnapshot{}
	if len(agentIDs) > 0 {
		ids := make([]pgtype.UUID, 0, len(agentIDs))
		for _, raw := range agentIDs {
			id, err := util.ParseUUID(raw)
			if err != nil {
				return routing.RoutingFacts{}, err
			}
			ids = append(ids, id)
		}
		agents, err := s.h.Queries.GetAgentsByIDs(ctx, ids)
		if err != nil {
			return routing.RoutingFacts{}, err
		}
		spans, err := s.h.Queries.ListRecentTaskSpansByAgents(ctx, ids)
		if err != nil {
			return routing.RoutingFacts{}, err
		}
		latencies := map[string][]int64{}
		for _, span := range spans {
			if !span.StartedAt.Valid || !span.CompletedAt.Valid {
				continue
			}
			ms := span.CompletedAt.Time.Sub(span.StartedAt.Time).Milliseconds()
			if ms < 0 {
				continue
			}
			latencies[util.UUIDToString(span.AgentID)] = append(latencies[util.UUIDToString(span.AgentID)], ms)
		}
		for _, agent := range agents {
			if agent.WorkspaceID != wsID || agent.Kind != "user" {
				continue
			}
			id := util.UUIDToString(agent.ID)
			var runtime *db.AgentRuntime
			if agent.RuntimeID.Valid {
				if row, ok := byRuntime[util.UUIDToString(agent.RuntimeID)]; ok {
					runtime = &row
				}
			}
			facts := routing.SeatFacts{
				AgentID:     id,
				Model:       strings.TrimSpace(agent.Model.String),
				Tier:        strings.TrimSpace(agent.RoutingTier.String),
				Archived:    agent.ArchivedAt.Valid,
				Cancelled:   agent.Status == "cancelled",
				WorkKnown:   true,
				WorkEnabled: agent.WorkEnabled,
				Runtime:     runtimeClass(agent, runtime),
				Quota:       quotaFromRuntime(runtime, now),
				Latencies:   latencies[id],
				ObservedAt:  now,
			}
			if !agent.Model.Valid {
				facts.Model = ""
			}
			if !agent.RoutingTier.Valid {
				facts.Tier = ""
			}
			seats[id] = routing.InterpretSeat(facts, now)
		}
	}

	return routing.RoutingFacts{
		Seats:     seats,
		Providers: providerQuotas(runtimes, providers, now),
	}, nil
}

func providerQuotas(runtimes []db.AgentRuntime, providers []string, now time.Time) []routing.ProviderQuota {
	wanted := map[string]bool{}
	for _, provider := range providers {
		wanted[strings.ToLower(strings.TrimSpace(provider))] = true
	}
	best := map[string]routing.QuotaObservation{}
	for _, runtime := range runtimes {
		key := strings.ToLower(strings.TrimSpace(runtime.Provider))
		if key == "" || !wanted[key] {
			continue
		}
		obs := quotaFromRuntime(&runtime, now)
		current, ok := best[key]
		if !ok || obs.ObservedAt.After(current.ObservedAt) {
			best[key] = obs
		}
	}
	out := make([]routing.ProviderQuota, 0, len(providers))
	seen := map[string]bool{}
	for _, provider := range providers {
		key := strings.ToLower(strings.TrimSpace(provider))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, routing.InterpretProvider(key, best[key], now))
	}
	return out
}

func quotaFromRuntime(runtime *db.AgentRuntime, now time.Time) routing.QuotaObservation {
	if runtime == nil {
		return routing.QuotaObservation{}
	}
	snapshot := planLimitsForResponse(runtime.PlanLimits)
	if snapshot == nil {
		return routing.QuotaObservation{}
	}
	return routing.ObserveQuota(snapshot.Status, time.Unix(snapshot.ObservedAt, 0).UTC(), planWindows(snapshot), now)
}

func planWindows(snapshot *protocol.PlanLimitsSnapshot) []routing.PlanWindow {
	if snapshot == nil {
		return nil
	}
	out := make([]routing.PlanWindow, 0, len(snapshot.Windows))
	for _, window := range snapshot.Windows {
		item := routing.PlanWindow{UsedPercent: window.UsedPercent, Remaining: window.Remaining}
		if window.ResetsAt != nil {
			reset := time.Unix(*window.ResetsAt, 0).UTC()
			item.ResetsAt = &reset
		}
		out = append(out, item)
	}
	return out
}

// runtimeClass is the routing view of a runtime: online, unreachable, or
// dead. It is narrower than admission — an offline machine still queues a
// task somebody assigned by hand, but routing will not hand a new ticket to
// a seat that cannot run it.
func runtimeClass(agent db.Agent, runtime *db.AgentRuntime) string {
	if !agent.RuntimeID.Valid || runtime == nil {
		return "dead"
	}
	if runtime.Visibility == "private" && runtime.OwnerID.Valid && (!agent.OwnerID.Valid || agent.OwnerID != runtime.OwnerID) {
		return "dead"
	}
	if reason, ok := readRuntimeOffline(runtime.Metadata); ok {
		switch reason.Code {
		case "not_executable":
			return "dead"
		case "dsh_profile":
			if !reason.Installing {
				return "dead"
			}
		}
	}
	switch runtime.Status {
	case "online":
		return "online"
	case "":
		return ""
	default:
		return "unreachable"
	}
}

type runtimeOfflineReason struct {
	Code       string `json:"code"`
	Installing bool   `json:"installing"`
}

func readRuntimeOffline(raw []byte) (runtimeOfflineReason, bool) {
	if len(raw) == 0 {
		return runtimeOfflineReason{}, false
	}
	var reason runtimeOfflineReason
	if err := json.Unmarshal(raw, &reason); err != nil || reason.Code == "" {
		return runtimeOfflineReason{}, false
	}
	return reason, true
}
