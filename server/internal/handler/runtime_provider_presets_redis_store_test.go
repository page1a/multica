package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Reuses the newRedisTestClient helper from
// runtime_local_skills_redis_store_test.go: same Redis instance, same gating
// on REDIS_TEST_URL, same FlushDB-per-test isolation.

// TestRedisProviderPresetStore_EnvelopePersistsRunStartedAt is a pure marshal
// round-trip: the `json:"-"` tag on RunStartedAt means the running-timeout
// escape hatch silently stops working across nodes unless the envelope
// re-promotes it.
func TestRedisProviderPresetStore_EnvelopePersistsRunStartedAt(t *testing.T) {
	store := &RedisProviderPresetStore{}
	now := time.Now().UTC().Truncate(time.Microsecond)
	req := &ProviderPresetRequest{
		ID:           "id-1",
		RuntimeID:    "rt-1",
		Provider:     "dsh",
		Action:       ProviderPresetActionUpsert,
		Payload:      providerPresetTestPayload(t, providerPresetTestKey),
		Status:       ProviderPresetRunning,
		CreatedAt:    now.Add(-time.Second),
		UpdatedAt:    now,
		RunStartedAt: &now,
	}
	data, err := store.marshalRequest(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := store.unmarshalRequest(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RunStartedAt == nil || !got.RunStartedAt.Equal(now) {
		t.Fatalf("RunStartedAt lost: %+v", got.RunStartedAt)
	}
	if got.Status != ProviderPresetRunning || got.ID != "id-1" || got.RuntimeID != "rt-1" {
		t.Fatalf("identifiers lost: %+v", got)
	}
}

func TestRedisProviderPresetStore_DeliversTheKeyOnceAndKeepsItNowhere(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisProviderPresetStore(rdb)

	req, err := store.Create(ctx, "runtime-1", "dsh", ProviderPresetActionUpsert, providerPresetTestPayload(t, providerPresetTestKey))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	polled, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if providerPresetPayloadHasKey(t, polled.Payload) {
		t.Fatalf("the poll response carried the key: %s", polled.Payload)
	}

	claimed, err := store.PopPending(ctx, "runtime-1")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if claimed == nil || claimed.ID != req.ID || !providerPresetPayloadHasKey(t, claimed.Payload) {
		t.Fatalf("claim did not deliver the key: %+v", claimed)
	}

	// The persisted copy is what a second node (and any later reader) sees.
	stored, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get after claim: %v", err)
	}
	if stored.Status != ProviderPresetRunning {
		t.Fatalf("status = %s", stored.Status)
	}
	if providerPresetPayloadHasKey(t, stored.Payload) {
		t.Fatalf("Redis kept the key: %s", stored.Payload)
	}
	raw, err := rdb.Get(ctx, providerPresetKey(req.ID)).Bytes()
	if err != nil {
		t.Fatalf("read raw record: %v", err)
	}
	if strings.Contains(string(raw), providerPresetTestKey) {
		t.Fatalf("the raw Redis value holds the key: %s", raw)
	}

	if err := store.Complete(ctx, req.ID, ProviderPresetResult{
		Providers: []ProviderPresetEntry{{ID: "p", HasKey: true}},
		Active:    &ProviderPresetActive{Provider: "p", Model: "m"},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	done, err := store.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get after complete: %v", err)
	}
	if done.Status != ProviderPresetCompleted || len(done.Providers) != 1 || done.Active == nil {
		t.Fatalf("completion not stored: %+v", done)
	}
}

func TestRedisProviderPresetStore_ClaimIsExclusiveAcrossNodes(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	nodeA := NewRedisProviderPresetStore(rdb)
	nodeB := NewRedisProviderPresetStore(rdb)

	req, err := nodeA.Create(ctx, "runtime-1", "dsh", ProviderPresetActionList, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	first, err := nodeA.PopPending(ctx, "runtime-1")
	if err != nil {
		t.Fatalf("node A pop: %v", err)
	}
	second, err := nodeB.PopPending(ctx, "runtime-1")
	if err != nil {
		t.Fatalf("node B pop: %v", err)
	}
	if first == nil || first.ID != req.ID {
		t.Fatalf("node A did not claim: %+v", first)
	}
	if second != nil {
		t.Fatalf("both nodes claimed the same request: %+v", second)
	}
}

func TestRedisProviderPresetStore_TimeoutIsPersistedForSiblings(t *testing.T) {
	rdb := newRedisTestClient(t)
	ctx := context.Background()
	store := NewRedisProviderPresetStore(rdb)

	req, err := store.Create(ctx, "runtime-1", "dsh", ProviderPresetActionList, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Backdate the record so the pending timeout applies.
	backdated := *req
	backdated.CreatedAt = time.Now().Add(-2 * providerPresetPendingTimeout)
	data, err := store.marshalRequest(&backdated)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := rdb.Set(ctx, providerPresetKey(req.ID), data, providerPresetStoreRetention).Err(); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	other := NewRedisProviderPresetStore(rdb)
	got, err := other.Get(ctx, req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != ProviderPresetTimeout {
		t.Fatalf("status = %s, want timeout", got.Status)
	}
	if pending, err := other.HasPending(ctx, "runtime-1"); err != nil || pending {
		t.Fatalf("a timed-out request is still claimable: %v (%v)", pending, err)
	}
}

func TestRedisProviderPresetStore_JSONShapeMatchesTheInMemoryStore(t *testing.T) {
	// A record decoded from Redis and one built in memory must serialize to
	// the same client-visible shape; otherwise the poll response depends on
	// which store the deployment runs.
	store := &RedisProviderPresetStore{}
	now := time.Now().UTC().Truncate(time.Microsecond)
	req := &ProviderPresetRequest{
		ID: "id-1", RuntimeID: "rt-1", Provider: "dsh", Action: ProviderPresetActionList,
		Status: ProviderPresetCompleted, Providers: []ProviderPresetEntry{{ID: "p"}},
		CreatedAt: now, UpdatedAt: now,
	}
	data, err := store.marshalRequest(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := store.unmarshalRequest(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	want, err := json.Marshal(redactedProviderPresetCopy(req))
	if err != nil {
		t.Fatalf("marshal in-memory record: %v", err)
	}
	fromRedis, err := json.Marshal(redactedProviderPresetCopy(got))
	if err != nil {
		t.Fatalf("marshal decoded record: %v", err)
	}
	if string(want) != string(fromRedis) {
		t.Fatalf("wire shapes differ:\n in-memory: %s\n redis:     %s", want, fromRedis)
	}
	if strings.Contains(string(fromRedis), `"run_started_at"`) || strings.Contains(string(fromRedis), `"s"`) {
		t.Fatalf("the persistence envelope leaked into the client shape: %s", fromRedis)
	}
}
