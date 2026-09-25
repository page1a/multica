package handler

import (
	"context"
	"testing"
)

func TestAgentCLICommandStoreKeepsFollowAndClearsOneUpdate(t *testing.T) {
	store := NewInMemoryAgentCLICommandStore()
	ctx := context.Background()
	follow := false
	if err := store.SetFollow(ctx, "rt-1", follow); err != nil {
		t.Fatal(err)
	}
	if err := store.SetUpdate(ctx, "rt-1", "req-1"); err != nil {
		t.Fatal(err)
	}
	cmd, err := store.Peek(ctx, "rt-1")
	if err != nil {
		t.Fatal(err)
	}
	if cmd == nil || cmd.Follow == nil || *cmd.Follow || !cmd.UpdateNow || cmd.RequestID != "req-1" {
		t.Fatalf("peek = %#v", cmd)
	}
	// A second peek still has the command. Heartbeats must be able to retry.
	again, err := store.Peek(ctx, "rt-1")
	if err != nil || again == nil || again.RequestID != "req-1" {
		t.Fatalf("second peek = %#v, %v", again, err)
	}
	if err := store.ClearUpdate(ctx, "rt-1", "other"); err != nil {
		t.Fatal(err)
	}
	kept, _ := store.Peek(ctx, "rt-1")
	if kept == nil || kept.RequestID != "req-1" {
		t.Fatalf("unrelated clear removed the request: %#v", kept)
	}
	if err := store.ClearUpdate(ctx, "rt-1", "req-1"); err != nil {
		t.Fatal(err)
	}
	left, _ := store.Peek(ctx, "rt-1")
	if left == nil || left.UpdateNow || left.Follow == nil || *left.Follow || left.FollowID == "" {
		t.Fatalf("follow should remain after the update is cleared: %#v", left)
	}
}

func TestAgentCLIFollowClickIsDeliveredOncePerWorkspace(t *testing.T) {
	store := NewInMemoryAgentCLICommandStore()
	ctx := context.Background()
	if err := store.SetFollow(ctx, "rt-a", false); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFollow(ctx, "rt-b", true); err != nil {
		t.Fatal(err)
	}
	if err := store.SetUpdate(ctx, "rt-b", "req-b"); err != nil {
		t.Fatal(err)
	}

	a, err := store.Peek(ctx, "rt-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Peek(ctx, "rt-b")
	if err != nil {
		t.Fatal(err)
	}
	if a == nil || b == nil || a.Follow == nil || b.Follow == nil || *a.Follow || !*b.Follow {
		t.Fatalf("a=%#v b=%#v", a, b)
	}
	if a.FollowID == "" || b.FollowID == "" || a.FollowID == b.FollowID {
		t.Fatalf("each workspace needs its own follow id: %#v %#v", a, b)
	}
	again, err := store.Peek(ctx, "rt-a")
	if err != nil || again == nil || again.FollowID != a.FollowID {
		t.Fatalf("peek consumed the follow click: %#v %v", again, err)
	}

	if err := store.ClearFollow(ctx, "rt-a", a.FollowID); err != nil {
		t.Fatal(err)
	}
	if left, _ := store.Peek(ctx, "rt-a"); left != nil {
		t.Fatalf("acked workspace still pending: %#v", left)
	}
	if still, _ := store.Peek(ctx, "rt-b"); still == nil || still.FollowID != b.FollowID || !still.UpdateNow {
		t.Fatalf("other workspace = %#v", still)
	}

	// A newer click on A must survive the late ack of the click it replaced.
	if err := store.SetFollow(ctx, "rt-a", true); err != nil {
		t.Fatal(err)
	}
	newer, err := store.Peek(ctx, "rt-a")
	if err != nil || newer == nil || newer.FollowID == a.FollowID || newer.Follow == nil || !*newer.Follow {
		t.Fatalf("newer click = %#v %v", newer, err)
	}
	if err := store.ClearFollow(ctx, "rt-a", a.FollowID); err != nil {
		t.Fatal(err)
	}
	if kept, _ := store.Peek(ctx, "rt-a"); kept == nil || kept.FollowID != newer.FollowID {
		t.Fatalf("stale ack removed the new click: %#v", kept)
	}

	if err := store.ClearFollow(ctx, "rt-b", b.FollowID); err != nil {
		t.Fatal(err)
	}
	clearedB, _ := store.Peek(ctx, "rt-b")
	if clearedB == nil || clearedB.Follow != nil || !clearedB.UpdateNow || clearedB.RequestID != "req-b" {
		t.Fatalf("clearing follow dropped the update: %#v", clearedB)
	}
	if err := store.ClearFollow(ctx, "rt-a", newer.FollowID); err != nil {
		t.Fatal(err)
	}
	if last, _ := store.Peek(ctx, "rt-a"); last != nil {
		t.Fatalf("workspace A still pending: %#v", last)
	}
}
