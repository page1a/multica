package main

import "testing"

func TestWaitingLinksKeepsOnlyUnansweredIncomingOffers(t *testing.T) {
	links := []any{
		map[string]any{"id": "wait", "side": "viewer", "status": "pending"},
		map[string]any{"id": "active", "side": "viewer", "status": "active"},
		map[string]any{"id": "ours", "side": "source", "status": "pending"},
	}
	got := waitingLinks(links)
	if len(got) != 1 || got[0].(map[string]any)["id"] != "wait" {
		t.Fatalf("waitingLinks = %v", got)
	}
	if got := waitingLinks(nil); len(got) != 0 {
		t.Fatalf("waitingLinks(nil) = %v", got)
	}
}
