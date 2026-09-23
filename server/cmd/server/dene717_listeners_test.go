package main

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// issue:invalidated is the one event whose routing is decided by its payload:
// a recipient_id makes it personal (one member's access changed), and its
// absence means the whole workspace is being told to refetch. Both shapes must
// reach exactly one place — never both.
func TestRegisterListeners_IssueInvalidatedRoutesByRecipient(t *testing.T) {
	t.Run("targeted", func(t *testing.T) {
		bus := events.New()
		fb := &fakeBroadcaster{}
		registerListeners(bus, fb)

		bus.Publish(events.Event{
			Type:        protocol.EventIssueInvalidated,
			WorkspaceID: "ws-1",
			ActorType:   "member",
			ActorID:     "actor-1",
			Payload: map[string]any{
				"issue_id":     "issue-1",
				"recipient_id": "user-1",
			},
		})

		if len(fb.workspaceCalls) != 0 {
			t.Fatalf("targeted invalidation reached workspace fanout: %+v", fb.workspaceCalls)
		}
		if len(fb.userCalls) != 1 || fb.userCalls[0].userID != "user-1" {
			t.Fatalf("user calls = %+v, want user-1 once", fb.userCalls)
		}
		var frame struct {
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		}
		if err := json.Unmarshal(fb.userCalls[0].msg, &frame); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		if frame.Type != protocol.EventIssueInvalidated || frame.Payload["issue_id"] != "issue-1" {
			t.Fatalf("frame = %+v", frame)
		}
		// Routing bookkeeping is not part of the wire contract.
		if _, ok := frame.Payload["recipient_id"]; ok {
			t.Fatalf("recipient_id leaked onto the wire: %+v", frame.Payload)
		}
	})

	t.Run("workspace_wide", func(t *testing.T) {
		bus := events.New()
		fb := &fakeBroadcaster{}
		registerListeners(bus, fb)

		bus.Publish(events.Event{
			Type:        protocol.EventIssueInvalidated,
			WorkspaceID: "ws-1",
			ActorType:   "member",
			ActorID:     "actor-1",
			Payload:     map[string]any{"project_id": "project-1"},
		})

		if len(fb.userCalls) != 0 {
			t.Fatalf("workspace-wide invalidation was sent personally: %+v", fb.userCalls)
		}
		if len(fb.workspaceCalls) != 1 || fb.workspaceCalls[0].workspaceID != "ws-1" {
			t.Fatalf("workspace calls = %+v, want ws-1 once", fb.workspaceCalls)
		}
	})
}
