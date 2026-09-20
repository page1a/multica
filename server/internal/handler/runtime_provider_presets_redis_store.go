package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis-backed implementation of ProviderPresetStore. The wire layout matches
// runtime_models_redis_store.go so the operational story is identical:
// namespaced keys, a ZSET-backed pending queue, and an atomic claim through the
// shared Lua script. A pending request that is only visible to the replica
// that received the POST would never be claimed by whichever node holds the
// daemon's heartbeat.
//
// Key layout:
//
//	mul:{runtime_pending}:provider_presets:req:<request_id>       → JSON-encoded ProviderPresetRequest, TTL = retention
//	mul:{runtime_pending}:provider_presets:pending:<runtime_id>   → ZSET { member = request_id, score = created_at UnixNano }
//	                                                                TTL = retention*2, so stale members can be swept lazily
//
// No persisted copy outlives the claim with an api_key in it: every write that
// follows the claim goes through persistRedacted, and PopPending persists the
// record it just handed to a heartbeat with the field stripped. The pending
// record is the one exception, by necessity — it is the buffer the key waits
// in for the daemon that has not asked yet.

const (
	providerPresetKeyPrefix          = "mul:" + runtimePendingRedisHashTag + ":provider_presets:req:"
	providerPresetPendingPrefix      = "mul:" + runtimePendingRedisHashTag + ":provider_presets:pending:"
	providerPresetRedisPopMaxRetries = 5
)

func providerPresetKey(id string) string { return providerPresetKeyPrefix + id }
func providerPresetPendingKey(runtimeID string) string {
	return providerPresetPendingPrefix + runtimeID
}

// RedisProviderPresetStore stores provider preset requests in Redis so every
// API node agrees on the same pending / running / terminal state.
type RedisProviderPresetStore struct {
	rdb redis.UniversalClient
}

func NewRedisProviderPresetStore(rdb redis.UniversalClient) *RedisProviderPresetStore {
	return &RedisProviderPresetStore{rdb: rdb}
}

func (s *RedisProviderPresetStore) Create(ctx context.Context, runtimeID, provider, action string, payload json.RawMessage) (*ProviderPresetRequest, error) {
	now := time.Now()
	req := &ProviderPresetRequest{
		ID:        randomID(),
		RuntimeID: runtimeID,
		Provider:  provider,
		Action:    action,
		Payload:   payload,
		Status:    ProviderPresetPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	data, err := s.marshalRequest(req)
	if err != nil {
		return nil, err
	}

	requestKey := providerPresetKey(req.ID)
	pendingKey := providerPresetPendingKey(runtimeID)
	// The request is not observable by a dispatcher until it is in the pending
	// set, so the two writes need no transaction — which also keeps this
	// working on managed Redis deployments that deny MULTI.
	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, requestKey, data, providerPresetStoreRetention)
	pipe.ZAdd(ctx, pendingKey, redis.Z{Score: float64(now.UnixNano()), Member: req.ID})
	pipe.Expire(ctx, pendingKey, providerPresetStoreRetention*2)
	if _, err := pipe.Exec(ctx); err != nil {
		_ = s.rdb.Del(ctx, requestKey).Err()
		_ = s.rdb.ZRem(ctx, pendingKey, req.ID).Err()
		return nil, fmt.Errorf("persist provider preset request: %w", err)
	}
	return req, nil
}

func (s *RedisProviderPresetStore) Get(ctx context.Context, id string) (*ProviderPresetRequest, error) {
	req, err := s.loadRequest(ctx, id)
	if err != nil || req == nil {
		return nil, err
	}
	return redactedProviderPresetCopy(req), nil
}

// loadRequest fetches one record, applies the timeout transitions, and
// persists a transition so sibling nodes observe the same terminal state.
func (s *RedisProviderPresetStore) loadRequest(ctx context.Context, id string) (*ProviderPresetRequest, error) {
	raw, err := s.rdb.Get(ctx, providerPresetKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get provider preset request: %w", err)
	}
	req, err := s.unmarshalRequest(raw)
	if err != nil {
		return nil, err
	}
	if applyProviderPresetTimeout(req, time.Now()) {
		if err := s.persistRedacted(ctx, req); err != nil {
			return nil, err
		}
		s.rdb.ZRem(ctx, providerPresetPendingKey(req.RuntimeID), req.ID)
	}
	return req, nil
}

func (s *RedisProviderPresetStore) persistRequest(ctx context.Context, req *ProviderPresetRequest) error {
	data, err := s.marshalRequest(req)
	if err != nil {
		return err
	}
	if err := s.rdb.Set(ctx, providerPresetKey(req.ID), data, providerPresetStoreRetention).Err(); err != nil {
		return fmt.Errorf("persist provider preset request: %w", err)
	}
	return nil
}

// persistRedacted is the write path for every record that has already been
// claimed or has stopped moving. Redacting here rather than at each call site
// keeps a new terminal transition from silently reintroducing the key.
func (s *RedisProviderPresetStore) persistRedacted(ctx context.Context, req *ProviderPresetRequest) error {
	req.Payload = redactProviderPresetPayload(req.Payload)
	return s.persistRequest(ctx, req)
}

// ProviderPresetRequest tags RunStartedAt as `json:"-"`, so Redis persistence
// has to re-promote it or every reader sees a nil start time and the
// running-timeout escape silently stops working across nodes.
type redisProviderPresetEnvelope struct {
	Public       *ProviderPresetRequest `json:"r"`
	RunStartedAt *time.Time             `json:"s,omitempty"`
}

// marshalRequest encodes a record, api_key and all. The pending record is the
// delivery buffer the daemon has not read yet, so Create persists through this
// one; every write after the claim uses persistRedacted.
func (s *RedisProviderPresetStore) marshalRequest(req *ProviderPresetRequest) ([]byte, error) {
	env := redisProviderPresetEnvelope{Public: req, RunStartedAt: req.RunStartedAt}
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("marshal provider preset request: %w", err)
	}
	return data, nil
}

func (s *RedisProviderPresetStore) unmarshalRequest(raw []byte) (*ProviderPresetRequest, error) {
	var env redisProviderPresetEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode provider preset request: %w", err)
	}
	if env.Public == nil {
		return nil, fmt.Errorf("decode provider preset request: missing payload")
	}
	env.Public.RunStartedAt = env.RunStartedAt
	return env.Public, nil
}

// HasPending is the cheap read-only probe the heartbeat hot path uses to gate
// the side-effecting PopPending.
func (s *RedisProviderPresetStore) HasPending(ctx context.Context, runtimeID string) (bool, error) {
	count, err := s.rdb.ZCard(ctx, providerPresetPendingKey(runtimeID)).Result()
	if err != nil {
		return false, fmt.Errorf("zcard provider preset pending: %w", err)
	}
	return count > 0, nil
}

func (s *RedisProviderPresetStore) PopPending(ctx context.Context, runtimeID string) (*ProviderPresetRequest, error) {
	pendingKey := providerPresetPendingKey(runtimeID)

	for attempt := 0; attempt < providerPresetRedisPopMaxRetries; attempt++ {
		ids, err := s.rdb.ZRange(ctx, pendingKey, 0, 0).Result()
		if err != nil {
			return nil, fmt.Errorf("zrange provider preset pending: %w", err)
		}
		if len(ids) == 0 {
			return nil, nil
		}
		id := ids[0]

		req, err := s.loadRequest(ctx, id)
		if err != nil {
			return nil, err
		}
		if req == nil {
			// The record expired while the zset still references it.
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}
		if req.Status != ProviderPresetPending {
			// The timeout fired in loadRequest, or another node claimed it.
			s.rdb.ZRem(ctx, pendingKey, id)
			continue
		}

		// Copy before touching the record: this copy is what the heartbeat
		// frame delivers, key and all. The write below persists the same
		// request with the key stripped.
		delivery := *req
		now := time.Now()
		req.Status = ProviderPresetRunning
		req.RunStartedAt = &now
		req.UpdatedAt = now
		req.Payload = redactProviderPresetPayload(req.Payload)
		data, err := s.marshalRequest(req)
		if err != nil {
			return nil, err
		}

		result, err := claimPendingScript.Run(
			ctx, s.rdb,
			[]string{pendingKey, providerPresetKey(id)},
			id, data, int(providerPresetStoreRetention.Seconds()),
		).Int64()
		if err != nil {
			return nil, fmt.Errorf("claim provider preset pending: %w", err)
		}
		if result == 0 {
			// Another node won the claim; retry for whatever else is queued.
			continue
		}
		return &delivery, nil
	}
	return nil, nil
}

func (s *RedisProviderPresetStore) Complete(ctx context.Context, id string, result ProviderPresetResult) error {
	req, err := s.loadRequest(ctx, id)
	if err != nil || req == nil {
		return err
	}
	req.Status = ProviderPresetCompleted
	req.Providers = result.Providers
	req.Active = result.Active
	req.ClearedActive = result.ClearedActive
	req.Models = result.Models
	req.UpdatedAt = time.Now()
	return s.persistRedacted(ctx, req)
}

func (s *RedisProviderPresetStore) Fail(ctx context.Context, id string, failure ProviderPresetFailure) error {
	req, err := s.loadRequest(ctx, id)
	if err != nil || req == nil {
		return err
	}
	req.Status = ProviderPresetFailed
	req.Error = failure.Message
	req.ErrorKind = failure.Kind
	req.ErrorParams = failure.Params
	req.UpdatedAt = time.Now()
	return s.persistRedacted(ctx, req)
}
