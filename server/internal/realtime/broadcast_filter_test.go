package realtime

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// probeConn is a connected client with a background reader: a test has to be
// able to assert "nothing arrived" and then keep using the same socket, and a
// gorilla read that hits its deadline is permanent.
type probeConn struct {
	t      *testing.T
	conn   *websocket.Conn
	frames chan []byte
	once   sync.Once
}

// connectWSAs is connectWS for a named recipient: the delivery filter answers
// per user, so a test needs two sockets that are not the same person.
func connectWSAs(t *testing.T, server *httptest.Server, userID string) *probeConn {
	t.Helper()
	token := makeTestTokenForUser(t, userID, "")
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?workspace_id=" + testWorkspaceID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("failed to connect WebSocket: %v", err)
	}
	authMsg, _ := json.Marshal(map[string]any{
		"type":    "auth",
		"payload": map[string]string{"token": token},
	})
	if err := conn.WriteMessage(websocket.TextMessage, authMsg); err != nil {
		t.Fatalf("failed to send auth message: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, ack, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read auth_ack: %v", err)
	}
	if !strings.Contains(string(ack), "auth_ack") {
		t.Fatalf("expected auth_ack, got %s", ack)
	}
	conn.SetReadDeadline(time.Time{})

	probe := &probeConn{t: t, conn: conn, frames: make(chan []byte, 64)}
	go probe.read()
	t.Cleanup(func() { probe.once.Do(func() { _ = conn.Close() }) })
	return probe
}

func (p *probeConn) read() {
	for {
		_, raw, err := p.conn.ReadMessage()
		if err != nil {
			close(p.frames)
			return
		}
		select {
		case p.frames <- raw:
		default:
		}
	}
}

func (p *probeConn) next(window time.Duration) ([]byte, bool) {
	p.t.Helper()
	select {
	case raw, ok := <-p.frames:
		return raw, ok
	case <-time.After(window):
		return nil, false
	}
}

func decodeProbeFrame(t *testing.T, raw []byte) struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
} {
	t.Helper()
	var frame struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode frame %s: %v", raw, err)
	}
	return frame
}

// The filter is the single decision point for per-recipient delivery: it must
// see the scope it is deciding for, and its answer — suppress, pass through or
// rewrite — must be what each recipient actually receives.
func TestBroadcastToScopeDedupAppliesFilterPerRecipient(t *testing.T) {
	hub, server := newTestHub(t)
	alice := connectWSAs(t, server, "alice")
	bob := connectWSAs(t, server, "bob")
	time.Sleep(150 * time.Millisecond)

	var mu sync.Mutex
	var scopes [][2]string
	hub.SetBroadcastFilter(func(_ context.Context, scopeType, scopeID string, frame []byte) BroadcastDecision {
		mu.Lock()
		scopes = append(scopes, [2]string{scopeType, scopeID})
		mu.Unlock()
		return func(userID string) ([]byte, bool) {
			if userID == "bob" {
				return nil, false
			}
			return []byte(`{"type":"probe","payload":{"narrowed":true}}`), true
		}
	})

	hub.BroadcastToScopeDedup(ScopeWorkspace, testWorkspaceID, []byte(`{"type":"probe"}`), "evt-filter")

	raw, ok := alice.next(2 * time.Second)
	if !ok {
		t.Fatal("allowed recipient received nothing")
	}
	if frame := decodeProbeFrame(t, raw); frame.Type != "probe" || frame.Payload["narrowed"] != true {
		t.Fatalf("allowed recipient got %s, want the filter's frame", raw)
	}
	if raw, ok := bob.next(400 * time.Millisecond); ok {
		t.Fatalf("suppressed recipient received %s", raw)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(scopes) != 1 || scopes[0] != [2]string{ScopeWorkspace, testWorkspaceID} {
		t.Fatalf("filter scopes = %+v, want one workspace decision", scopes)
	}
}

// A filter that suppresses everyone must deliver nothing and must not disable
// the hub: the next broadcast has to reach its recipients.
func TestBroadcastToScopeDedupSuppressingEveryoneDeliversNothing(t *testing.T) {
	hub, server := newTestHub(t)
	alice := connectWSAs(t, server, "alice")
	time.Sleep(150 * time.Millisecond)

	hub.SetBroadcastFilter(func(context.Context, string, string, []byte) BroadcastDecision {
		return func(string) ([]byte, bool) { return nil, false }
	})

	hub.BroadcastToScopeDedup(ScopeWorkspace, testWorkspaceID, []byte(`{"type":"probe"}`), "evt-suppress")
	if raw, ok := alice.next(400 * time.Millisecond); ok {
		t.Fatalf("suppressed frame was delivered: %s", raw)
	}

	hub.SetBroadcastFilter(nil)
	hub.BroadcastToScopeDedup(ScopeWorkspace, testWorkspaceID, []byte(`{"type":"probe"}`), "evt-after")
	if _, ok := alice.next(2 * time.Second); !ok {
		t.Fatal("hub stopped delivering after a fully suppressed broadcast")
	}
}

// nil decision means "no recipient-specific rule applies" and the original
// frame must arrive untouched — the pass-through half of the contract.
func TestBroadcastToScopeDedupNilDecisionDeliversOriginal(t *testing.T) {
	hub, server := newTestHub(t)
	alice := connectWSAs(t, server, "alice")
	time.Sleep(150 * time.Millisecond)

	hub.SetBroadcastFilter(func(context.Context, string, string, []byte) BroadcastDecision {
		return nil
	})

	hub.BroadcastToScopeDedup(ScopeWorkspace, testWorkspaceID, []byte(`{"type":"probe","payload":{"original":true}}`), "evt-nil")
	raw, ok := alice.next(2 * time.Second)
	if !ok {
		t.Fatal("nil decision suppressed the frame")
	}
	if !strings.Contains(string(raw), `"original":true`) {
		t.Fatalf("frame = %s, want the original bytes", raw)
	}
}

// A personal frame runs through the same decision point, so an inbox item for
// a resource the recipient may not see is suppressed by the same rule that
// filters the workspace room.
func TestSendToUserAppliesFilter(t *testing.T) {
	hub, server := newTestHub(t)
	alice := connectWSAs(t, server, "alice")
	time.Sleep(150 * time.Millisecond)

	var mu sync.Mutex
	var scopeTypes []string
	hub.SetBroadcastFilter(func(_ context.Context, scopeType, scopeID string, _ []byte) BroadcastDecision {
		mu.Lock()
		scopeTypes = append(scopeTypes, scopeType)
		mu.Unlock()
		if scopeID != "alice" {
			return nil
		}
		return func(string) ([]byte, bool) { return nil, false }
	})

	hub.SendToUser("alice", []byte(`{"type":"inbox:new"}`))
	if raw, ok := alice.next(400 * time.Millisecond); ok {
		t.Fatalf("personal frame was delivered despite the filter: %s", raw)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(scopeTypes) != 1 || scopeTypes[0] != ScopeUser {
		t.Fatalf("filter scopes = %+v, want one user-scope decision", scopeTypes)
	}
}
