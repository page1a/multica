package service

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Clients patch the cached agent row only when agent:status carries the
// top-level agent_id + status pair, so the task-driven flip must set both
// (DENE-1504). The full "agent" map stays for older clients.
func TestPublishAgentStatusMarksPureStatusFlip(t *testing.T) {
	bus := events.New()
	var got events.Event
	bus.Subscribe(protocol.EventAgentStatus, func(e events.Event) { got = e })
	s := &TaskService{Bus: bus}

	agentID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	s.publishAgentStatus(db.Agent{ID: agentID, WorkspaceID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, Status: "working"})

	payload, ok := got.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload = %T, want map", got.Payload)
	}
	if payload["agent_id"] != "01000000-0000-0000-0000-000000000000" || payload["status"] != "working" {
		t.Fatalf("payload marker = %v / %v", payload["agent_id"], payload["status"])
	}
	if _, ok := payload["agent"]; !ok {
		t.Fatal("payload dropped the legacy agent map")
	}
}
