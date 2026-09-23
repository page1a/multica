package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// devinBlockedArgs are flags hardcoded by the daemon that must not be overridden
// by user-configured custom_args. `acp` is the protocol subcommand. Devin ACP
// does not take a root `--permission-mode`; auto-approve via ACP
// session/request_permission. `-p`/`--print` would leave ACP stdio.
var devinBlockedArgs = map[string]blockedArgMode{
	"acp":               blockedStandalone,
	"--yolo":            blockedStandalone,
	"--permission-mode": blockedWithValue,
	"--auto-approve":    blockedStandalone,
	"--approval-mode":   blockedWithValue,
	"-p":                blockedStandalone,
	"--print":           blockedStandalone,
	"--mode":            blockedWithValue,
	"--output-format":   blockedWithValue,
	"--model":           blockedWithValue,
}

// devinReaderDrainGrace bounds how long the turn waits for trailing ACP
// notifications after the session/prompt response. A var so tests can shorten it.
var devinReaderDrainGrace = 2 * time.Second

// devinBackend implements Backend by spawning `devin acp` and communicating
// via the standard ACP JSON-RPC 2.0 transport over stdin/stdout.
//
// Host-local Devin CLI only (`devin acp`). Cloud Devin VMs, Playbooks, and
// org Secrets are out of scope. There is no root `--permission-mode`; the
// shared ACP client auto-approves session/request_permission. Model selection
// is the `devin acp --model` flag, not session/set_model.
type devinBackend struct {
	cfg Config
}

func (b *devinBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "devin"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("devin executable not found at %q: %w", execPath, err)
	}

	mcpServers, err := buildACPMcpServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("devin: invalid mcp_config: %w", err)
	}

	timeout := opts.Timeout
	runCtx, cancel := runContext(ctx, timeout)

	devinArgs := []string{"acp"}
	if model := strings.TrimSpace(opts.Model); model != "" {
		devinArgs = append(devinArgs, "--model", model)
	}
	devinArgs = append(devinArgs, filterCustomArgs(opts.CustomArgs, devinBlockedArgs, b.cfg.Logger)...)
	cmd := b.cfg.commandAt(execPath).exec(runCtx, devinArgs...)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(devinArgs, trustAgentCommandPositional(0, "acp")))
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("devin stdout pipe: %w", err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("devin stdin pipe: %w", err)
	}
	providerErr := newACPProviderErrorSniffer("devin")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("devin stderr pipe: %w", err)
	}

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start devin: %w", err)
	}

	stderrSink := io.MultiWriter(newLogWriter(b.cfg.Logger, "[devin:stderr] "), providerErr)
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrSink, stderr)
	}()

	b.cfg.Logger.Info("devin acp started", "pid", cmd.Process.Pid, "cwd", opts.Cwd)

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)

	var deliverable acpDeliverableTracker
	var streamingCurrentTurn atomic.Bool

	promptDone := make(chan hermesPromptResult, 1)
	activity := make(chan struct{}, 1)

	c := &hermesClient{
		cfg:          b.cfg,
		stdin:        stdin,
		pending:      make(map[int]*pendingRPC),
		pendingTools: make(map[string]*pendingToolCall),
		acceptNotification: func(string) bool {
			return streamingCurrentTurn.Load()
		},
		onActivity: func() {
			select {
			case activity <- struct{}{}:
			default:
			}
		},
		onMessage: func(msg Message) {
			if !streamingCurrentTurn.Load() {
				return
			}
			if msg.Type == MessageToolUse {
				msg.Tool = kimiToolNameFromTitle(msg.Tool)
			}
			deliverable.observe(msg)
			trySend(msgCh, msg)
		},
		onPromptDone: func(result hermesPromptResult) {
			if !streamingCurrentTurn.Load() {
				return
			}
			select {
			case promptDone <- result:
			default:
			}
		},
	}

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		scanner := newAgentStreamScanner(stdout)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			c.handleLine(line)
		}
		c.closeAllPending(fmt.Errorf("devin process exited"))
	}()

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)
		defer func() {
			stdin.Close()
			_ = cmd.Wait()
			releaseProcessGroup(cmd)
		}()

		startTime := time.Now()
		finalStatus := "completed"
		var finalError string
		var sessionID string
		var resumeRejected bool
		effectiveModel := strings.TrimSpace(opts.Model)

		initResult, err := c.request(runCtx, "initialize", map[string]any{
			"protocolVersion": 1,
			"clientInfo": map[string]any{
				"name":    "multica-agent-sdk",
				"version": "0.2.0",
			},
			"clientCapabilities": map[string]any{},
		})
		if err != nil {
			finalStatus = "failed"
			finalError = fmt.Sprintf("devin initialize failed: %v", err)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		}

		mcpServers = filterACPMcpServersByCapability(mcpServers, extractACPMcpCapabilities(initResult), "devin", b.cfg)

		// Desktop-spawned `devin acp` waits for authenticate when initialize
		// advertises authMethods. Standalone often advertises nothing (skip)
		// and uses the stored `devin auth` login. Interactive browser login
		// cannot be completed from the daemon, so that method is skipped.
		if methodID, authErr := selectDevinAuthMethod(extractACPAuthMethods(initResult), envHasNonEmpty(cmd.Env, "WINDSURF_API_KEY")); authErr != nil {
			finalStatus = "failed"
			finalError = fmt.Sprintf("devin authentication setup failed: %v", authErr)
			resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
			return
		} else if methodID != "" {
			authParams := map[string]any{"methodId": methodID}
			if methodID == devinAuthMethodWindsurfAPIKey {
				authParams["_meta"] = map[string]any{"headless": true}
			}
			if _, err := c.request(runCtx, "authenticate", authParams); err != nil {
				finalStatus = "failed"
				finalError = fmt.Sprintf("devin authenticate (%s) failed: %v — run `devin auth login` or set WINDSURF_API_KEY", methodID, err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			b.cfg.Logger.Info("devin authenticated", "method", methodID)
		}

		cwd := opts.Cwd
		if cwd == "" {
			cwd = "."
		}

		if opts.ResumeSessionID != "" {
			result, err := c.request(runCtx, "session/load", map[string]any{
				"cwd":        cwd,
				"sessionId":  opts.ResumeSessionID,
				"mcpServers": mcpServers,
			})
			if err != nil {
				finalStatus, finalError, resumeRejected = classifyACPResumeFailure(
					runCtx, "devin", "session/load", err, timeout, b.cfg.Logger)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds(), ResumeRejected: resumeRejected}
				return
			}
			var changed bool
			sessionID, changed = resolveResumedSessionID(opts.ResumeSessionID, result)
			if changed {
				b.cfg.Logger.Warn("agent returned a different session id on resume — original was likely lost; continuing with the new id",
					"backend", "devin",
					"requested", opts.ResumeSessionID,
					"actual", sessionID,
				)
			}
			if effectiveModel == "" {
				effectiveModel = extractACPCurrentModelID(result)
			}
		} else {
			result, err := c.request(runCtx, "session/new", map[string]any{
				"cwd":        cwd,
				"mcpServers": mcpServers,
			})
			if err != nil {
				finalStatus = "failed"
				finalError = fmt.Sprintf("devin session/new failed: %v", err)
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			sessionID = extractACPSessionID(result)
			if sessionID == "" {
				finalStatus = "failed"
				finalError = "devin session/new returned no session ID"
				resCh <- Result{Status: finalStatus, Error: finalError, DurationMs: time.Since(startTime).Milliseconds()}
				return
			}
			if effectiveModel == "" {
				effectiveModel = extractACPCurrentModelID(result)
			}
		}

		c.sessionID = sessionID
		b.cfg.Logger.Info("devin session created", "session_id", sessionID)

		userText := prompt
		if opts.SystemPrompt != "" {
			userText = opts.SystemPrompt + "\n\n---\n\n" + prompt
		}

		// Pin after setup succeeds and before the prompt, so a daemon restart
		// mid-turn can resume. Model is already fixed by the launch flag.
		streamingCurrentTurn.Store(true)
		trySend(msgCh, Message{Type: MessageStatus, Status: "running", SessionID: sessionID})

		_, err = c.request(runCtx, "session/prompt", map[string]any{
			"sessionId": sessionID,
			"prompt": []map[string]any{
				{"type": "text", "text": userText},
			},
		})
		if err != nil {
			if runCtx.Err() == context.DeadlineExceeded {
				finalStatus = "timeout"
				finalError = fmt.Sprintf("devin timed out after %s", timeout)
			} else if runCtx.Err() == context.Canceled {
				finalStatus = "aborted"
				finalError = "execution cancelled"
			} else {
				finalStatus = "failed"
				finalError = fmt.Sprintf("devin session/prompt failed: %v", err)
				if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
					b.cfg.Logger.Warn("resumed session not found at prompt time; clearing session id so the daemon retries fresh",
						"backend", "devin",
						"session_id", sessionID,
					)
					sessionID = ""
					resumeRejected = true
				}
			}
		} else {
			select {
			case pr := <-promptDone:
				if pr.stopReason == "cancelled" {
					finalStatus = "aborted"
					finalError = "devin cancelled the prompt"
				}
				c.mergeUsage(pr.usage)
			default:
			}
			waitForACPNotificationQuiescence(runCtx, activity, readerDone, acpNotificationQuietTime, devinReaderDrainGrace)
		}

		duration := time.Since(startTime)
		b.cfg.Logger.Info("devin finished", "pid", cmd.Process.Pid, "status", finalStatus, "duration", duration.Round(time.Millisecond).String())

		stdin.Close()
		cancel()

		<-readerDone
		<-stderrDone

		finalOutput, providerErrorOutput := deliverable.result()
		finalStatus, finalError = promoteACPResultOnProviderError(finalStatus, finalError, providerErrorOutput, providerErr)

		u := c.accumulatedUsage()
		var usageMap map[string]TokenUsage
		if acpUsagePresent(u) {
			model := effectiveModel
			if model == "" {
				model = "unknown"
			}
			usageMap = map[string]TokenUsage{model: u}
		}

		resCh <- Result{
			Status:         finalStatus,
			Output:         finalOutput,
			Error:          finalError,
			DurationMs:     duration.Milliseconds(),
			SessionID:      sessionID,
			ResumeRejected: resumeRejected,
			Usage:          usageMap,
		}
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

func (b *devinBackend) applyBuiltinRuntimeOverrides(desc BuiltinRuntime) {
	if desc.DefaultExecutable != "" && b.cfg.ExecutablePath == "" {
		b.cfg.ExecutablePath = desc.DefaultExecutable
	}
}

const devinAuthMethodWindsurfAPIKey = "windsurf-api-key"

// selectDevinAuthMethod picks an ACP authenticate method advertised by
// initialize. Empty methods means skip (standalone `devin acp` often uses
// local `devin auth` without an explicit handshake).
func selectDevinAuthMethod(methods []string, haveWindsurfKey bool) (string, error) {
	if len(methods) == 0 {
		return "", nil
	}
	offered := make(map[string]bool, len(methods))
	for _, m := range methods {
		if m = strings.TrimSpace(m); m != "" {
			offered[m] = true
		}
	}
	if haveWindsurfKey && offered[devinAuthMethodWindsurfAPIKey] {
		return devinAuthMethodWindsurfAPIKey, nil
	}
	for _, candidate := range []string{"devin-cli", "cached-login", "local", "login"} {
		if offered[candidate] {
			return candidate, nil
		}
	}
	// Standalone `devin acp` advertises interactive browser login. The daemon
	// cannot complete that; skip the handshake and rely on stored `devin auth`
	// or WINDSURF_API_KEY. Browser plus windsurf-api-key without a key still
	// skips — it is not "windsurf-only".
	if offered["devin-browser"] {
		return "", nil
	}
	if offered[devinAuthMethodWindsurfAPIKey] {
		return "", fmt.Errorf("devin acp advertised only windsurf-api-key; set WINDSURF_API_KEY or run `devin auth login`")
	}
	advertised := make([]string, 0, len(offered))
	for method := range offered {
		advertised = append(advertised, method)
	}
	sort.Strings(advertised)
	return "", fmt.Errorf("devin acp advertised unsupported auth methods %q", advertised)
}

// discoverDevinModels runs `devin models list --format json`.
// Unknown or empty output degrades to an empty catalog (manual entry).
func discoverDevinModels(ctx context.Context, runtimeCmd Command) ([]Model, error) {
	if runtimeCmd.Path == "" {
		runtimeCmd.Path = "devin"
	}
	if _, err := exec.LookPath(runtimeCmd.Path); err != nil {
		return []Model{}, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := runtimeCmd.exec(runCtx, "models", "list", "--format", "json")
	hideAgentWindow(cmd)
	stdout, err := cmd.Output()
	if err != nil || len(stdout) == 0 {
		return []Model{}, nil
	}
	return parseDevinModels(stdout)
}

func parseDevinModels(data []byte) ([]Model, error) {
	type entry struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Label    string `json:"label"`
		Provider string `json:"provider"`
		Model    string `json:"model"`
		ModelUID string `json:"model_uid"`
	}
	var families struct {
		Families []struct {
			Slug     string  `json:"slug"`
			Variants []entry `json:"variants"`
		} `json:"families"`
	}
	if err := json.Unmarshal(data, &families); err == nil && len(families.Families) > 0 {
		out := make([]Model, 0, 64)
		seen := map[string]bool{}
		for _, fam := range families.Families {
			provider := strings.TrimSpace(fam.Slug)
			for _, e := range fam.Variants {
				id := strings.TrimSpace(e.ModelUID)
				if id == "" {
					id = strings.TrimSpace(e.ID)
				}
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				label := strings.TrimSpace(e.Label)
				if label == "" {
					label = strings.TrimSpace(e.Name)
				}
				if label == "" {
					label = id
				}
				out = append(out, Model{ID: id, Label: label, Provider: provider})
			}
		}
		if len(out) > 0 {
			return out, nil
		}
	}
	var models []entry
	var wrapper struct {
		Models []entry `json:"models"`
	}
	if err := json.Unmarshal(data, &wrapper); err == nil && len(wrapper.Models) > 0 {
		models = wrapper.Models
	} else if err := json.Unmarshal(data, &models); err != nil {
		return []Model{}, nil
	}
	out := make([]Model, 0, len(models))
	seen := map[string]bool{}
	for _, e := range models {
		id := strings.TrimSpace(e.ID)
		if id == "" {
			id = strings.TrimSpace(e.ModelUID)
		}
		if id == "" {
			id = strings.TrimSpace(e.Model)
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		label := strings.TrimSpace(e.Name)
		if label == "" {
			label = strings.TrimSpace(e.Label)
		}
		if label == "" {
			label = id
		}
		out = append(out, Model{ID: id, Label: label, Provider: strings.TrimSpace(e.Provider)})
	}
	return out, nil
}
