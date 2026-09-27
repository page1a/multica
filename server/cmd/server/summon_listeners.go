package main

import (
	"context"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// registerSummonListeners closes calls a ticket moved past without a reply
// (DENE-901). Every status and owner write publishes issue:updated, so one
// listener covers the web UI, the CLI, close and routing alike.
func registerSummonListeners(bus *events.Bus, queries *db.Queries) {
	ctx := context.Background()
	bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		statusChanged, _ := payload["status_changed"].(bool)
		assigneeChanged, _ := payload["assignee_changed"].(bool)
		if !statusChanged && !assigneeChanged {
			return
		}
		issueID := ""
		switch issue := payload["issue"].(type) {
		case handler.IssueResponse:
			issueID = issue.ID
		case *handler.IssueResponse:
			if issue != nil {
				issueID = issue.ID
			}
		case map[string]any:
			issueID, _ = issue["id"].(string)
		}
		id, err := util.ParseUUID(issueID)
		if err != nil {
			return
		}
		service.CloseSummonsOnProgress(ctx, queries, bus, id, e.ActorType, e.ActorID, statusChanged, assigneeChanged)
	})
}
