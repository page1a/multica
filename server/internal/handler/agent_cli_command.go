package handler

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/redis/go-redis/v9"
)

// AgentCLICommandStore holds a follow switch or a one-shot upgrade until the
// daemon reports that it applied them. Peek does not consume the command: a
// missed heartbeat has to be able to deliver it again.
type AgentCLICommandStore interface {
	SetFollow(ctx context.Context, runtimeID string, follow bool) error
	SetUpdate(ctx context.Context, runtimeID, requestID string) error
	Peek(ctx context.Context, runtimeID string) (*protocol.DaemonHeartbeatPendingAgentCLI, error)
	ClearUpdate(ctx context.Context, runtimeID, requestID string) error
	ClearFollow(ctx context.Context, runtimeID, followID string) error
}

type agentCLICommandRecord struct {
	Follow    *bool  `json:"follow,omitempty"`
	FollowID  string `json:"follow_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

func (r agentCLICommandRecord) pending() *protocol.DaemonHeartbeatPendingAgentCLI {
	if r.Follow == nil && r.RequestID == "" {
		return nil
	}
	cmd := &protocol.DaemonHeartbeatPendingAgentCLI{}
	if r.Follow != nil {
		cmd.Follow = r.Follow
		cmd.FollowID = r.FollowID
	}
	if r.RequestID != "" {
		cmd.UpdateNow = true
		cmd.RequestID = r.RequestID
	}
	return cmd
}

type InMemoryAgentCLICommandStore struct {
	mu   sync.Mutex
	cmds map[string]agentCLICommandRecord
}

func NewInMemoryAgentCLICommandStore() *InMemoryAgentCLICommandStore {
	return &InMemoryAgentCLICommandStore{cmds: map[string]agentCLICommandRecord{}}
}

func (s *InMemoryAgentCLICommandStore) SetFollow(_ context.Context, runtimeID string, follow bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.cmds[runtimeID]
	rec.Follow = &follow
	rec.FollowID = randomID()
	s.cmds[runtimeID] = rec
	return nil
}

func (s *InMemoryAgentCLICommandStore) SetUpdate(_ context.Context, runtimeID, requestID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.cmds[runtimeID]
	rec.RequestID = requestID
	s.cmds[runtimeID] = rec
	return nil
}

func (s *InMemoryAgentCLICommandStore) Peek(_ context.Context, runtimeID string) (*protocol.DaemonHeartbeatPendingAgentCLI, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmds[runtimeID].pending(), nil
}

func (s *InMemoryAgentCLICommandStore) ClearUpdate(_ context.Context, runtimeID, requestID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.cmds[runtimeID]
	if !ok || requestID == "" || rec.RequestID != requestID {
		return nil
	}
	rec.RequestID = ""
	if rec.Follow == nil {
		delete(s.cmds, runtimeID)
		return nil
	}
	s.cmds[runtimeID] = rec
	return nil
}

func (s *InMemoryAgentCLICommandStore) ClearFollow(_ context.Context, runtimeID, followID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.cmds[runtimeID]
	if !ok || followID == "" || rec.FollowID != followID {
		return nil
	}
	rec.Follow = nil
	rec.FollowID = ""
	if rec.RequestID == "" {
		delete(s.cmds, runtimeID)
		return nil
	}
	s.cmds[runtimeID] = rec
	return nil
}

const agentCLICommandKeyPrefix = "mul:agent_cli_cmd:"

// RedisAgentCLICommandStore shares the command across API nodes. A lost race
// between two clicks is last-write-wins, which is the right outcome for a
// follow switch and for "update now".
type RedisAgentCLICommandStore struct {
	rdb redis.UniversalClient
}

func NewRedisAgentCLICommandStore(rdb redis.UniversalClient) *RedisAgentCLICommandStore {
	return &RedisAgentCLICommandStore{rdb: rdb}
}

func agentCLICommandKey(runtimeID string) string {
	return agentCLICommandKeyPrefix + runtimeID
}

func (s *RedisAgentCLICommandStore) load(ctx context.Context, runtimeID string) (agentCLICommandRecord, error) {
	raw, err := s.rdb.Get(ctx, agentCLICommandKey(runtimeID)).Bytes()
	if err == redis.Nil {
		return agentCLICommandRecord{}, nil
	}
	if err != nil {
		return agentCLICommandRecord{}, err
	}
	var rec agentCLICommandRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return agentCLICommandRecord{}, nil
	}
	return rec, nil
}

func (s *RedisAgentCLICommandStore) save(ctx context.Context, runtimeID string, rec agentCLICommandRecord) error {
	if rec.Follow == nil && rec.RequestID == "" {
		return s.rdb.Del(ctx, agentCLICommandKey(runtimeID)).Err()
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, agentCLICommandKey(runtimeID), raw, 0).Err()
}

func (s *RedisAgentCLICommandStore) SetFollow(ctx context.Context, runtimeID string, follow bool) error {
	rec, err := s.load(ctx, runtimeID)
	if err != nil {
		return err
	}
	rec.Follow = &follow
	rec.FollowID = randomID()
	return s.save(ctx, runtimeID, rec)
}

func (s *RedisAgentCLICommandStore) SetUpdate(ctx context.Context, runtimeID, requestID string) error {
	rec, err := s.load(ctx, runtimeID)
	if err != nil {
		return err
	}
	rec.RequestID = requestID
	return s.save(ctx, runtimeID, rec)
}

func (s *RedisAgentCLICommandStore) Peek(ctx context.Context, runtimeID string) (*protocol.DaemonHeartbeatPendingAgentCLI, error) {
	rec, err := s.load(ctx, runtimeID)
	if err != nil {
		return nil, err
	}
	return rec.pending(), nil
}

func (s *RedisAgentCLICommandStore) ClearUpdate(ctx context.Context, runtimeID, requestID string) error {
	rec, err := s.load(ctx, runtimeID)
	if err != nil {
		return err
	}
	if requestID == "" || rec.RequestID != requestID {
		return nil
	}
	rec.RequestID = ""
	return s.save(ctx, runtimeID, rec)
}

func (s *RedisAgentCLICommandStore) ClearFollow(ctx context.Context, runtimeID, followID string) error {
	rec, err := s.load(ctx, runtimeID)
	if err != nil {
		return err
	}
	if followID == "" || rec.FollowID != followID {
		return nil
	}
	rec.Follow = nil
	rec.FollowID = ""
	return s.save(ctx, runtimeID, rec)
}
