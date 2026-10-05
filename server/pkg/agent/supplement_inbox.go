package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// supplementInboxDirEnv names the private directory a provider-side extension
// (the OpenCode plugin, the Pi extension) reads task supplements from.
const supplementInboxDirEnv = "MULTICA_SUPPLEMENT_DIR"

const supplementInboxPollInterval = 100 * time.Millisecond

// supplementInbox hands task supplements to an extension running inside the
// provider process, for CLIs whose own stdio protocol has no in-turn input.
//
// Each supplement is one file. The daemon publishes <id>.txt atomically; the
// extension claims it by renaming it to <id>.claimed and, once the model has
// consumed it inside the active turn, writes <id>.ok (or <id>.err with a
// reason). Withdrawal is the same rename race in reverse: the daemon renames
// <id>.txt to <id>.cancelled, and whichever rename wins owns the outcome. An
// input the extension claimed is therefore never reported failed while it may
// still reach the model, and an unclaimed one never reaches it after failure.
type supplementInbox struct {
	dir      string
	provider string
	done     chan struct{}
	inflight sync.WaitGroup

	mu     sync.Mutex
	active bool
	ended  bool
	seq    uint64
}

func newSupplementInbox(provider string) (*supplementInbox, error) {
	dir, err := os.MkdirTemp("", "multica-supplement-")
	if err != nil {
		return nil, fmt.Errorf("%s supplement inbox: %w", provider, err)
	}
	return &supplementInbox{dir: dir, provider: provider, done: make(chan struct{})}, nil
}

// writeExtension places the provider extension source inside the inbox, so its
// lifetime and permissions follow the inbox directory.
func (i *supplementInbox) writeExtension(name, source string) (string, error) {
	path := filepath.Join(i.dir, name)
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		return "", fmt.Errorf("%s supplement extension: %w", i.provider, err)
	}
	return path, nil
}

// start is nil-safe. It marks the provider turn as confirmed by its own output stream.
func (i *supplementInbox) start() {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.ended {
		i.active = true
	}
}

func (i *supplementInbox) ready() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.active && !i.ended
}

func (i *supplementInbox) deliver(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	i.mu.Lock()
	if i.ended {
		i.mu.Unlock()
		return context.Canceled
	}
	if !i.active {
		i.mu.Unlock()
		return fmt.Errorf("%s turn has not started", i.provider)
	}
	i.seq++
	id := fmt.Sprintf("%020d", i.seq)
	i.inflight.Add(1)
	i.mu.Unlock()
	defer i.inflight.Done()

	base := filepath.Join(i.dir, id)
	if err := os.WriteFile(base+".tmp", []byte(text), 0o600); err != nil {
		return fmt.Errorf("%s supplement write: %w", i.provider, err)
	}
	if err := os.Rename(base+".tmp", base+".txt"); err != nil {
		return fmt.Errorf("%s supplement publish: %w", i.provider, err)
	}

	ticker := time.NewTicker(supplementInboxPollInterval)
	defer ticker.Stop()
	cancelled := ctx.Done()
	for {
		if err, ok := i.outcome(base); ok {
			return err
		}
		select {
		case <-ticker.C:
			continue
		case <-cancelled:
			if os.Rename(base+".txt", base+".cancelled") == nil {
				return ctx.Err()
			}
			// The extension already owns it; its acknowledgement decides.
			cancelled = nil
		case <-i.done:
			if err, ok := i.outcome(base); ok {
				return err
			}
			// The provider exited. A claimed input that was never acknowledged
			// cannot be consumed any more, so the turn ended without it.
			return context.Canceled
		}
	}
}

func (i *supplementInbox) outcome(base string) (error, bool) {
	if _, err := os.Stat(base + ".ok"); err == nil {
		return nil, true
	}
	reason, err := os.ReadFile(base + ".err")
	if err != nil {
		return nil, false
	}
	detail := strings.TrimSpace(string(reason))
	if strings.Contains(detail, "turn ended") {
		return context.Canceled, true
	}
	return errors.New(i.provider + " rejected the supplement: " + detail), true
}

// close is nil-safe. It ends admission once the provider process has exited, settles every
// pending delivery, and removes the inbox.
func (i *supplementInbox) close() {
	if i == nil {
		return
	}
	i.mu.Lock()
	if i.ended {
		i.mu.Unlock()
		return
	}
	i.ended, i.active = true, false
	close(i.done)
	i.mu.Unlock()
	i.inflight.Wait()
	_ = os.RemoveAll(i.dir)
}
