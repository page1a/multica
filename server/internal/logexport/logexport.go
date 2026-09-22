// Package logexport builds the single-file, redacted log export bundle that
// the web app, the desktop app, and `multica logs export` all consume.
//
// One generator, three clients. The server produces the bundle once and every
// client hands it back verbatim, so the dialog and the CLI cannot drift apart
// and a bundle pushed to a workspace git repo is the same artifact that was
// previewed in the UI.
//
// Everything in this package is pure: the caller reads the database rows and
// this package decides the time window, scopes the runs, masks secrets, and
// writes the AI summary. That separation is what lets the redaction and
// summary rules be tested without a database.
package logexport

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// FormatName and FormatVersion identify the artifact schema. A consumer that
// sees a different name/version must refuse rather than guess at the shape.
const (
	FormatName    = "multica.log-export"
	FormatVersion = 1
)

// DefaultMaxEntries bounds how many transcript entries one bundle carries. A
// long-lived issue in "whole task" scope can own an unbounded transcript, and
// the bundle has to survive a comment attachment and a git push on an instance
// whose upload path is slow. Hitting the cap sets Truncated so the reader
// knows the bundle is a head, not the whole story.
const DefaultMaxEntries = 20000

// maxRedactDepth bounds the recursive walk over a decoded tool input. Tool
// inputs arrive from the daemon as attacker-influenced JSON; the same bound
// (and the same fail-closed direction) as pkg/redact applies here.
const maxRedactDepth = 32

// truncatedPlaceholder replaces anything nested past maxRedactDepth. Dropping
// the value is the fail-safe direction: returning it raw would defeat the
// masking this package exists to perform.
const truncatedPlaceholder = "[REDACTED DEPTH LIMIT]"

// ScopeKind is the export range the caller asked for.
type ScopeKind string

const (
	// ScopeRun exports only the requested run's transcript.
	ScopeRun ScopeKind = "run"
	// ScopeHours exports every run of the requested run's issue whose entries
	// fall inside the last N hours.
	ScopeHours ScopeKind = "hours"
	// ScopeTask exports the whole run history of the requested run's issue.
	ScopeTask ScopeKind = "task"
)

// MinHours and MaxHours bound the --hours window so a typo cannot ask the
// database for a century of transcript.
const (
	MinHours = 1
	MaxHours = 24 * 30
)

// Scope is a validated (kind, hours) pair.
type Scope struct {
	Kind  ScopeKind `json:"kind"`
	Hours int       `json:"hours,omitempty"`
}

// ParseScope validates a wire/CLI scope string. An empty value defaults to the
// single run, which is the narrowest and cheapest read.
func ParseScope(raw string, hours int) (Scope, error) {
	kind := ScopeKind(strings.TrimSpace(raw))
	if kind == "" {
		kind = ScopeRun
	}
	switch kind {
	case ScopeRun, ScopeTask:
		if hours != 0 {
			return Scope{}, fmt.Errorf("--hours only applies to the %q scope", ScopeHours)
		}
		return Scope{Kind: kind}, nil
	case ScopeHours:
		if hours < MinHours || hours > MaxHours {
			return Scope{}, fmt.Errorf("--hours must be between %d and %d for the %q scope", MinHours, MaxHours, ScopeHours)
		}
		return Scope{Kind: kind, Hours: hours}, nil
	default:
		return Scope{}, fmt.Errorf("unknown scope %q (want %q, %q or %q)", raw, ScopeRun, ScopeHours, ScopeTask)
	}
}

// Label is the human-facing range name used in the summary and the artifact.
func (s Scope) Label() string {
	switch s.Kind {
	case ScopeHours:
		return fmt.Sprintf("最近 %d 小时", s.Hours)
	case ScopeTask:
		return "整个任务"
	default:
		return "本次运行"
	}
}

// Run is the metadata of one agent run (one agent_task_queue row).
type Run struct {
	TaskID        string
	AgentID       string
	AgentName     string
	Status        string
	StartedAt     *time.Time
	CompletedAt   *time.Time
	ExitCode      *int
	FailureReason string
	Error         string
}

// Target identifies what the caller asked to export: one run, plus the issue
// that owns it.
type Target struct {
	TaskID          string
	IssueID         string
	IssueIdentifier string
	IssueTitle      string
	AgentID         string
	AgentName       string
}

// Message is one structured transcript entry. It mirrors the task_message row
// but carries no database types, so this package stays testable without a DB.
type Message struct {
	TaskID          string
	Seq             int32
	At              time.Time
	Type            string
	Tool            string
	Content         string
	Input           any
	Output          string
	OutputTruncated bool
}

// Input is everything Build needs. Runs must be ordered oldest-first; Messages
// may arrive in any order. Env is the exported runs' environment: Masker masks
// the literal values of the deny-listed variables so a value with no
// recognizable token shape still cannot reach the artifact.
type Input struct {
	GeneratedAt time.Time
	Scope       Scope
	Target      Target
	Runs        []Run
	Messages    []Message
	Env         map[string]string
	// EnvLookupGap is non-empty when the runs' environment could not be read
	// completely, so the value deny-list could not run in full. The artifact
	// carries this forward instead of claiming a redaction that never
	// happened: pattern matching alone does not cover a password with no
	// token shape, and a bundle whose summary says "已自动脱敏" must not be
	// lying about it.
	EnvLookupGap string
	MaxEntries   int
}

// Entry is the artifact's per-log-line shape.
type Entry struct {
	Seq             int32  `json:"seq"`
	TaskID          string `json:"task_id"`
	At              string `json:"at"`
	Type            string `json:"type"`
	Tool            string `json:"tool,omitempty"`
	Content         string `json:"content,omitempty"`
	Input           any    `json:"input,omitempty"`
	Output          string `json:"output,omitempty"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
}

// RunView is the artifact's per-run metadata shape.
type RunView struct {
	TaskID        string `json:"task_id"`
	AgentID       string `json:"agent_id,omitempty"`
	AgentName     string `json:"agent_name,omitempty"`
	Status        string `json:"status"`
	StartedAt     string `json:"started_at,omitempty"`
	CompletedAt   string `json:"completed_at,omitempty"`
	ExitCode      *int   `json:"exit_code"`
	FailureReason string `json:"failure_reason,omitempty"`
	Error         string `json:"error,omitempty"`
}

// Window is the covered time span after the scope was applied.
type Window struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// TaskView is the artifact header: who ran, on what, over what range.
type TaskView struct {
	ID              string `json:"id"`
	IssueID         string `json:"issue_id,omitempty"`
	IssueIdentifier string `json:"issue_identifier,omitempty"`
	IssueTitle      string `json:"issue_title,omitempty"`
	AgentID         string `json:"agent_id,omitempty"`
	AgentName       string `json:"agent_name,omitempty"`
	Status          string `json:"status,omitempty"`
	ExitCode        *int   `json:"exit_code"`
	Scope           Scope  `json:"scope"`
	Window          Window `json:"window"`
}

// RedactionView is the artifact's statement about its own masking. It exists so
// a reader — or a tool that decides whether a bundle is safe to hand to an AI —
// can tell a fully redacted bundle from one that only got pattern matching,
// rather than trusting the summary's word for it.
type RedactionView struct {
	// PatternRules is true whenever the shared token/password patterns ran.
	// They run unconditionally, so it is always true in a built bundle.
	PatternRules bool `json:"pattern_rules"`
	// EnvDenyList is false when the exported runs' environment was not
	// available, which leaves known secret variable values unmasked.
	EnvDenyList bool `json:"env_deny_list"`
	// Complete is the field to gate on: false means the bundle may still
	// contain a credential and must not be forwarded blindly.
	Complete bool   `json:"complete"`
	Note     string `json:"note,omitempty"`
}

// Bundle is the exported artifact. It is what the endpoint returns, what the
// CLI writes to disk, and what the git push commits.
type Bundle struct {
	Format          string        `json:"format"`
	Version         int           `json:"version"`
	GeneratedAt     string        `json:"generated_at"`
	Task            TaskView      `json:"task"`
	Redaction       RedactionView `json:"redaction"`
	RunCount        int           `json:"run_count"`
	EntryCount      int           `json:"entry_count"`
	Truncated       bool          `json:"truncated"`
	Runs            []RunView     `json:"runs"`
	Entries         []Entry       `json:"entries"`
	SummaryMarkdown string        `json:"summary_markdown"`
}

// ErrUnknownScope is returned by Build when the input carries a scope this
// build does not understand.
var ErrUnknownScope = errors.New("unknown export scope")

// SelectRuns returns the runs a scope covers, oldest-first. `target` is the
// requested run and `runs` is the owning issue's full history.
//
// A run scope narrows to the requested run. Hours and task scopes widen to the
// issue's history, because the operator asking "what else happened" is exactly
// the one who found a single run inconclusive.
func SelectRuns(scope Scope, target Run, runs []Run) []Run {
	if scope.Kind == ScopeRun {
		return []Run{target}
	}
	if len(runs) == 0 {
		return []Run{target}
	}
	out := make([]Run, len(runs))
	copy(out, runs)
	sort.SliceStable(out, func(i, j int) bool {
		return runStart(out[i]).Before(runStart(out[j]))
	})
	return out
}

func runStart(r Run) time.Time {
	if r.StartedAt != nil {
		return *r.StartedAt
	}
	if r.CompletedAt != nil {
		return *r.CompletedAt
	}
	return time.Time{}
}

// windowFor computes the covered span. For a run scope the span is the run's
// own lifetime; for hours it is the requested lookback; for task it is the
// earliest entry through the generation time.
func windowFor(scope Scope, in Input, target Run, entries []Entry) Window {
	to := in.GeneratedAt.UTC()
	switch scope.Kind {
	case ScopeHours:
		from := to.Add(-time.Duration(scope.Hours) * time.Hour)
		return Window{From: from.Format(time.RFC3339), To: to.Format(time.RFC3339)}
	case ScopeTask:
		if from, ok := earliestEntry(entries); ok {
			return Window{From: from.Format(time.RFC3339), To: to.Format(time.RFC3339)}
		}
		return Window{To: to.Format(time.RFC3339)}
	default:
		from := target.StartedAt
		if from == nil {
			if earliest, ok := earliestEntry(entries); ok {
				from = &earliest
			}
		}
		end := to
		if target.CompletedAt != nil {
			end = target.CompletedAt.UTC()
		}
		w := Window{To: end.Format(time.RFC3339)}
		if from != nil {
			w.From = from.UTC().Format(time.RFC3339)
		}
		return w
	}
}

func earliestEntry(entries []Entry) (time.Time, bool) {
	var (
		found bool
		min   time.Time
	)
	for _, e := range entries {
		at, err := time.Parse(time.RFC3339, e.At)
		if err != nil {
			continue
		}
		if !found || at.Before(min) {
			min, found = at, true
		}
	}
	return min, found
}

// Build assembles the bundle: it applies the scope window, masks every secret,
// orders the entries, and writes the AI summary.
func Build(in Input) (Bundle, error) {
	scope := in.Scope
	if scope.Kind == "" {
		scope = Scope{Kind: ScopeRun}
	}
	switch scope.Kind {
	case ScopeRun, ScopeHours, ScopeTask:
	default:
		return Bundle{}, fmt.Errorf("%w: %q", ErrUnknownScope, scope.Kind)
	}

	generatedAt := in.GeneratedAt
	if generatedAt.IsZero() {
		generatedAt = time.Now()
	}
	in.GeneratedAt = generatedAt

	mask := NewMasker(in.Env)
	runOrder := make(map[string]int, len(in.Runs))
	for i, r := range in.Runs {
		if _, ok := runOrder[r.TaskID]; !ok {
			runOrder[r.TaskID] = i
		}
	}

	kept := make([]Message, 0, len(in.Messages))
	var from time.Time
	if scope.Kind == ScopeHours {
		from = generatedAt.Add(-time.Duration(scope.Hours) * time.Hour)
	}
	for _, m := range in.Messages {
		if scope.Kind == ScopeHours && m.At.Before(from) {
			continue
		}
		kept = append(kept, m)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		oi, iok := runOrder[kept[i].TaskID]
		oj, jok := runOrder[kept[j].TaskID]
		if iok && jok && oi != oj {
			return oi < oj
		}
		if !kept[i].At.Equal(kept[j].At) {
			return kept[i].At.Before(kept[j].At)
		}
		return kept[i].Seq < kept[j].Seq
	})

	maxEntries := in.MaxEntries
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	truncated := false
	if len(kept) > maxEntries {
		kept = kept[len(kept)-maxEntries:]
		truncated = true
	}

	entries := make([]Entry, len(kept))
	for i, m := range kept {
		entries[i] = Entry{
			Seq:             m.Seq,
			TaskID:          m.TaskID,
			At:              m.At.UTC().Format(time.RFC3339),
			Type:            m.Type,
			Tool:            m.Tool,
			Content:         mask.Text(m.Content),
			Input:           mask.Value(m.Input),
			Output:          mask.Text(m.Output),
			OutputTruncated: m.OutputTruncated,
		}
	}

	runs := make([]RunView, 0, len(in.Runs))
	targetRun := targetRunOf(in)
	window := windowFor(scope, in, targetRun, entries)
	for _, r := range in.Runs {
		runs = append(runs, RunView{
			TaskID:        r.TaskID,
			AgentID:       r.AgentID,
			AgentName:     r.AgentName,
			Status:        r.Status,
			StartedAt:     formatTimePtr(r.StartedAt),
			CompletedAt:   formatTimePtr(r.CompletedAt),
			ExitCode:      r.ExitCode,
			FailureReason: r.FailureReason,
			Error:         mask.Text(r.Error),
		})
	}

	bundle := Bundle{
		Format:      FormatName,
		Version:     FormatVersion,
		GeneratedAt: generatedAt.UTC().Format(time.RFC3339),
		Task: TaskView{
			ID:              in.Target.TaskID,
			IssueID:         in.Target.IssueID,
			IssueIdentifier: in.Target.IssueIdentifier,
			IssueTitle:      in.Target.IssueTitle,
			AgentID:         in.Target.AgentID,
			AgentName:       in.Target.AgentName,
			Status:          targetRun.Status,
			ExitCode:        targetRun.ExitCode,
			Scope:           scope,
			Window:          window,
		},
		RunCount:   len(runs),
		EntryCount: len(entries),
		Truncated:  truncated,
		Runs:       runs,
		Entries:    entries,
	}
	bundle.Redaction = RedactionView{
		PatternRules: true,
		EnvDenyList:  in.EnvLookupGap == "",
		Complete:     in.EnvLookupGap == "",
		Note:         in.EnvLookupGap,
	}
	bundle.SummaryMarkdown = Summarize(bundle)
	return bundle, nil
}

// targetRunOf finds the requested run among the scoped runs. For a run scope
// the list holds it alone; for hours/task the list is start-ordered, so the
// requested run is not necessarily first.
func targetRunOf(in Input) Run {
	for _, r := range in.Runs {
		if r.TaskID == in.Target.TaskID {
			return r
		}
	}
	if len(in.Runs) > 0 {
		return in.Runs[0]
	}
	return Run{}
}

// Marshal renders the artifact. Indented rather than compact on purpose: the
// bundle is meant to be opened, diffed in a git repo, and pasted into an AI
// chat, and the extra bytes are negligible next to the transcript itself.
func Marshal(b Bundle) ([]byte, error) {
	body, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// FileName is the artifact's suggested file name. It is stable for a given
// (issue, run, scope, time) so a git push of the same export does not churn.
func FileName(b Bundle) string {
	subject := b.Task.IssueIdentifier
	if subject == "" {
		subject = shortID(b.Task.ID)
	}
	run := shortID(b.Task.ID)
	scope := string(b.Task.Scope.Kind)
	if b.Task.Scope.Kind == ScopeHours {
		scope = fmt.Sprintf("last%dh", b.Task.Scope.Hours)
	}
	stamp := strings.NewReplacer(":", "", "-", "").Replace(b.GeneratedAt)
	return fmt.Sprintf("log-export-%s-%s-%s-%s.json", sanitizeFileToken(subject), sanitizeFileToken(run), scope, stamp)
}

func sanitizeFileToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "task"
	}
	return out
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
