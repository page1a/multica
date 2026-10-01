package routing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// This file is the analysis role (DENE-923).
//
// The judge is a finite-answer module: it is good at "which of these four",
// and poor at reading a ticket. Fed the raw title and a clipped body, its
// confidence sat in the 0.5–0.7 band on real tickets, because most of what it
// was given was prose it could not use. The analysis role is the step in
// front of it: a general model reads the whole ticket once and reduces it to
// four facts, and those facts are what the judge is asked about.
//
// The facts are cached on the issue and recomputed only when the ticket's
// title or body changes, and a ticket an agent creates can carry them from
// the start, in which case no analysis call is made at all.

// Fact values. They are a closed set on purpose: the judge answers best when
// the state it reads is made of choices, not sentences.
const (
	ScopeSmall       = "small"
	ScopeModule      = "module"
	ScopeCrossModule = "cross_module"

	ClarityClear = "clear"
	ClarityVague = "vague"

	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"
)

// Facts is what the analysis model reduces a ticket to.
type Facts struct {
	// Scope is how much of the codebase the change touches.
	Scope string `json:"scope"`
	// Clarity is whether the requirement can be acted on as written.
	Clarity string `json:"clarity"`
	// Risk is what getting it wrong costs.
	Risk string `json:"risk"`
	// NeedsHuman is whether a person has to make a call before or at
	// acceptance.
	NeedsHuman bool `json:"needs_human"`
	// Summary is one sentence for a human reader. It is never parsed.
	Summary string `json:"summary,omitempty"`
}

// Normalize lower-cases and trims the enum fields. It does not repair an
// unknown value: Valid is what decides whether the facts are usable.
func (f Facts) Normalize() Facts {
	f.Scope = strings.ToLower(strings.TrimSpace(f.Scope))
	f.Clarity = strings.ToLower(strings.TrimSpace(f.Clarity))
	f.Risk = strings.ToLower(strings.TrimSpace(f.Risk))
	f.Summary = strings.TrimSpace(f.Summary)
	return f
}

// Valid reports whether every enum field holds a known value.
func (f Facts) Valid() bool {
	switch f.Scope {
	case ScopeSmall, ScopeModule, ScopeCrossModule:
	default:
		return false
	}
	switch f.Clarity {
	case ClarityClear, ClarityVague:
	default:
		return false
	}
	switch f.Risk {
	case RiskLow, RiskMedium, RiskHigh:
	default:
		return false
	}
	return true
}

// Where a set of facts came from, for the decision comment.
const (
	FactsFromAnalysis = "analysis"
	FactsFromCreator  = "creator"
)

// AnalysisRecord is what is cached on the issue: the facts, the analysis
// model's own tier pick when it made one, and the fingerprint of the content
// they were derived from.
type AnalysisRecord struct {
	Facts Facts `json:"facts"`
	// Verdict is the analysis model's tier pick. Absent on facts a creator
	// supplied: those carry no pick, and the analysis-only mode derives one
	// from the facts by rule instead (see RuleVerdict).
	Verdict *Verdict `json:"verdict,omitempty"`
	// Source is FactsFromAnalysis or FactsFromCreator.
	Source string `json:"source"`
	// Model is the analysis model that wrote the record. A record from a
	// different model is recomputed; a creator's record has none.
	Model string `json:"model,omitempty"`
	// Hash is ContentHash of the ticket the record describes.
	Hash string `json:"hash"`
}

// AnalysisMetadataKey is the issue-metadata key the record is cached under.
// The value is the record as a JSON string: issue metadata only holds
// scalars.
const AnalysisMetadataKey = "routing.analysis"

// ContentHash fingerprints the part of a ticket the facts are derived from.
// Status, assignee and labels are left out on purpose: none of them changes
// what the work is, and every status flip would otherwise recompute.
func ContentHash(title, description string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(title) + "\n\x00\n" + strings.TrimSpace(description)))
	return hex.EncodeToString(sum[:8])
}

// ParseAnalysisRecord reads a cached record. Anything unreadable is absent.
func ParseAnalysisRecord(raw string) (AnalysisRecord, bool) {
	if strings.TrimSpace(raw) == "" {
		return AnalysisRecord{}, false
	}
	var rec AnalysisRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return AnalysisRecord{}, false
	}
	rec.Facts = rec.Facts.Normalize()
	if !rec.Facts.Valid() || rec.Hash == "" {
		return AnalysisRecord{}, false
	}
	return rec, true
}

// Encode renders the record for the metadata column.
func (rec AnalysisRecord) Encode() string {
	raw, _ := json.Marshal(rec)
	return string(raw)
}

// Fresh reports whether the record still describes this ticket as analysed
// by this model. A creator's record is not tied to a model.
func (rec AnalysisRecord) Fresh(issue Issue, model string) bool {
	if rec.Hash == "" || rec.Hash != issue.ContentHash {
		return false
	}
	if rec.Source == FactsFromCreator {
		return true
	}
	return strings.TrimSpace(rec.Model) == strings.TrimSpace(model)
}

// AnalysisState is what the analysis model reads: the judge's state plus the
// whole body, which never reaches the judge.
type AnalysisState struct {
	JudgeState
	Description string `json:"description"`
	// Discussion is the recent conversation on the ticket, oldest first and
	// clipped. It is read only for the blocked row, where "where is it stuck"
	// usually lives in the comments rather than in the body.
	Discussion []string `json:"discussion,omitempty"`
}

// Analyst is the analysis role. Like Judge, implementations must not write
// anything anywhere.
type Analyst interface {
	// Analyze reduces a ticket to facts and picks a tier from them.
	Analyze(ctx context.Context, target Target, st AnalysisState) (AnalysisRecord, error)
	// Stuck answers the blocked-row question and writes the "where is it
	// stuck" summary into Advice.Stuck.
	Stuck(ctx context.Context, target Target, st AnalysisState) (Advice, error)
}

// RuntimePromptRunner is the daemon-backed transport used by analysis when
// the workspace selected a subscribed runtime. The runner returns the raw JSON
// produced by the CLI; parsing and validation stay in this package so both
// transports have identical semantics.
type RuntimePromptRunner interface {
	Run(ctx context.Context, target Target, prompt string) (string, error)
}

type analysisRequestContextKey struct{}

// WithAnalysisRequest carries the ticket identity alongside a runtime probe.
// The daemon may finish after the routing waiter timed out, so the handler
// needs this identity to persist a late result safely.
func WithAnalysisRequest(ctx context.Context, workspaceID, issueID, contentHash string) context.Context {
	return context.WithValue(ctx, analysisRequestContextKey{}, [3]string{workspaceID, issueID, contentHash})
}

func AnalysisRequestFromContext(ctx context.Context) (workspaceID, issueID, contentHash string, ok bool) {
	v, ok := ctx.Value(analysisRequestContextKey{}).([3]string)
	if !ok {
		return "", "", "", false
	}
	return v[0], v[1], v[2], true
}

const analyzeSystemPrompt = `You read a work ticket and reduce it to facts, then route it to a seat on a fixed ladder of AI agents.

Facts — choose exactly one value for each:
- scope: "small" (one file or a local edit), "module" (one module or feature area), "cross_module" (several modules, a contract between them, or a migration).
- clarity: "clear" (can be acted on as written), "vague" (the goal or acceptance is open to interpretation).
- risk: "low" (cheap to get wrong and easy to undo), "medium", "high" (data loss, money, security, outward-facing or irreversible effects).
- needs_human: true only when a person has to make a decision the ticket does not already contain.
- summary: one short sentence for a human reader.

Routing:
1. executor_tier: which ladder tier should DO this work. Choose from candidate_tiers exactly.
2. reviewer: "seat" (an agent checks it), "human" (acceptance needs a conversation with a person), or "none" (small, self-evident work).
3. reviewer_tier: when reviewer is "seat", which tier checks it. Choose from candidate_tiers exactly.

When escalation_reason is present, the seat now holding the ticket reported that the work is too hard for it: choose again, weighing that report, and do not choose a weaker tier than the one that reported it.

Report calibrated confidence in [0,1] separately for the executor choice and the reviewer choice; below-threshold answers are discarded rather than used, so do not inflate them. Follow policy_prompt in the user payload when choosing tiers.

Respond with a JSON object with keys: scope, clarity, risk, needs_human, summary, executor_tier, executor_confidence, reviewer, reviewer_tier, reviewer_confidence, reason. You do not change status, assignee, or any other ticket field, and you do not take an action.`

const stuckSystemPrompt = `A work ticket is blocked. Read it and its recent discussion and say where it is stuck.

stuck: one or two short sentences naming the concrete thing it is waiting on, for a human reader.
cause: "tier" if the assigned seat is probably not strong enough, "human" if a person has to make a call, "other" otherwise.
suggested_tier: when cause is "tier", which tier to try instead, chosen from candidate_tiers exactly.
reason: one sentence of judgement and one of suggestion.

You are not changing anything on the ticket. Respond with a JSON object with keys: stuck, cause, suggested_tier, reason.`

// LLMAnalyst runs the analysis role on an OpenAI-compatible chat endpoint:
// the deployment gateway, or the workspace's own when both halves are set.
type LLMAnalyst struct {
	Gen     TextGenerator
	Dial    func(baseURL, apiKey string) TextGenerator
	Runtime RuntimePromptRunner
}

func (a LLMAnalyst) generator(t Target) TextGenerator {
	return LLMJudge{Gen: a.Gen, Dial: a.Dial}.generator(t)
}

// Available mirrors LLMJudge.Available for the settings section.
func (a LLMAnalyst) Available(t Target) bool {
	return LLMJudge{Gen: a.Gen, Dial: a.Dial}.Available(t)
}

func (a LLMAnalyst) ask(ctx context.Context, target Target, system string, st any, maxTokens int64) (string, error) {
	payload, err := json.Marshal(st)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrJudgeUnavailable, err)
	}
	if target.UsesRuntime() {
		if a.Runtime == nil {
			return "", ErrJudgeUnavailable
		}
		return a.Runtime.Run(ctx, target, system+"\n\n"+string(payload))
	}
	gen := a.generator(target)
	if gen == nil {
		return "", ErrJudgeUnavailable
	}
	raw, err := gen.GenerateJSON(ctx, target.Model, system, string(payload), 0, maxTokens)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrJudgeUnavailable, err)
	}
	return raw, nil
}

// Analyze asks for the facts and the tier pick in one call. Facts outside the
// closed set are a broken contract, not a weak answer.
func (a LLMAnalyst) Analyze(ctx context.Context, target Target, st AnalysisState) (AnalysisRecord, error) {
	raw, err := a.ask(ctx, target, analyzeSystemPrompt, st, 600)
	if err != nil {
		return AnalysisRecord{}, err
	}
	// Runtime CLIs wrap the model's JSON differently from the API gateway:
	// Claude returns a result envelope and Codex may emit JSONL events. Keep
	// that transport detail at the boundary so the rest of routing consumes the
	// same facts object for both sources.
	raw = unwrapRuntimeAnalysis(raw)
	return ParseAnalysisResult(raw, target.Model)
}

// ParseAnalysisResult validates a runtime or gateway response at the common
// routing boundary. It is also used by the daemon callback to persist a result
// that arrives after the original routing waiter has timed out.
func ParseAnalysisResult(raw, model string) (AnalysisRecord, error) {
	var reply struct {
		Facts
		Verdict
	}
	raw = unwrapRuntimeAnalysis(raw)
	if err := json.Unmarshal([]byte(raw), &reply); err != nil {
		return AnalysisRecord{}, fmt.Errorf("%w: analysis was not JSON: %v", ErrJudgeUnavailable, err)
	}
	facts := reply.Facts.Normalize()
	if !facts.Valid() {
		return AnalysisRecord{}, fmt.Errorf("%w: analysis facts outside the allowed values: %+v", ErrJudgeUnavailable, facts)
	}
	v := reply.Verdict
	v.Reviewer = ReviewerKind(strings.ToLower(strings.TrimSpace(string(v.Reviewer))))
	switch v.Reviewer {
	case ReviewerSeat, ReviewerHuman, ReviewerNone:
	default:
		return AnalysisRecord{}, fmt.Errorf("%w: unknown reviewer branch %q", ErrJudgeUnavailable, v.Reviewer)
	}
	return AnalysisRecord{Facts: facts, Verdict: &v, Source: FactsFromAnalysis, Model: model}, nil
}

// unwrapRuntimeAnalysis extracts the model message from the common CLI
// envelopes. It intentionally returns the input unchanged when it is already
// the facts object, preserving the historical API-gateway behaviour.
func unwrapRuntimeAnalysis(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) >= 2 {
			lines = lines[1:]
			if last := len(lines) - 1; strings.HasPrefix(strings.TrimSpace(lines[last]), "```") {
				lines = lines[:last]
			}
			raw = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &obj) == nil {
		// Claude's --output-format json envelope.
		if result, ok := obj["result"]; ok {
			var text string
			if json.Unmarshal(result, &text) == nil && strings.TrimSpace(text) != "" {
				return unwrapRuntimeAnalysis(text)
			}
		}
		// A few runtimes expose the final assistant message under item.text.
		if item, ok := obj["item"]; ok {
			var itemObj map[string]json.RawMessage
			if json.Unmarshal(item, &itemObj) == nil {
				if text, ok := itemObj["text"]; ok {
					var s string
					if json.Unmarshal(text, &s) == nil && strings.TrimSpace(s) != "" {
						return unwrapRuntimeAnalysis(s)
					}
				}
			}
		}
		// Already a plain facts object (or an unknown object): let the normal
		// validation produce the useful error message.
		return raw
	}
	if !strings.Contains(raw, "\n") {
		return raw
	}
	// Codex --json and similar modes can emit one JSON object per line. Use the
	// last line that contains a textual assistant result; progress events are
	// ignored.
	var candidate string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if unwrapped := unwrapRuntimeAnalysis(line); unwrapped != line {
			candidate = unwrapped
		}
	}
	if candidate != "" {
		return candidate
	}
	return raw
}

// Stuck asks the blocked-row question with the stuck summary.
func (a LLMAnalyst) Stuck(ctx context.Context, target Target, st AnalysisState) (Advice, error) {
	raw, err := a.ask(ctx, target, stuckSystemPrompt, st, 400)
	if err != nil {
		return Advice{}, err
	}
	var adv Advice
	if err := json.Unmarshal([]byte(raw), &adv); err != nil {
		return Advice{}, fmt.Errorf("%w: advice was not JSON: %v", ErrJudgeUnavailable, err)
	}
	adv.Cause = strings.ToLower(strings.TrimSpace(adv.Cause))
	adv.Stuck = strings.TrimSpace(adv.Stuck)
	return adv, nil
}

// RuleVerdict picks a tier from facts without a model. It is what the
// analysis-only mode uses for facts a creator supplied: the point of
// supplying them is to skip the analysis call, and in that mode there is no
// judge to ask either. The rule is the policy prompt's own reading of the
// facts, so it is written down once here rather than left to a model.
func RuleVerdict(f Facts) Verdict {
	tier := "medium"
	switch {
	case f.Risk == RiskHigh || f.Scope == ScopeCrossModule || f.Clarity == ClarityVague:
		tier = "strong"
	case f.Scope == ScopeSmall && f.Risk == RiskLow && f.Clarity == ClarityClear:
		tier = "weak"
	}
	v := Verdict{
		ExecutorTier:       tier,
		ExecutorConfidence: 1,
		Reviewer:           ReviewerSeat,
		ReviewerTier:       tier,
		ReviewerConfidence: 1,
		Reason:             "按建票时带的事实定档：" + FactsLine(f),
	}
	if f.NeedsHuman {
		v.Reviewer = ReviewerHuman
		v.ReviewerTier = ""
	}
	return v
}

// FactsLine renders facts the way the decision comment prints them.
func FactsLine(f Facts) string {
	scope := map[string]string{ScopeSmall: "小", ScopeModule: "单模块", ScopeCrossModule: "跨模块"}[f.Scope]
	clarity := map[string]string{ClarityClear: "清楚", ClarityVague: "模糊"}[f.Clarity]
	risk := map[string]string{RiskLow: "低", RiskMedium: "中", RiskHigh: "高"}[f.Risk]
	human := "否"
	if f.NeedsHuman {
		human = "是"
	}
	return fmt.Sprintf("改动范围 %s · 需求 %s · 出错代价 %s · 要人拍板 %s", scope, clarity, risk, human)
}

// AnalysisCache is the optional half of Store that persists analysis
// records. A store without it still routes; it just analyses every time.
type AnalysisCache interface {
	SaveAnalysis(ctx context.Context, workspaceID, issueID string, rec AnalysisRecord) error
}

// DiscussionReader is the optional half of Store that supplies the recent
// conversation for the stuck summary.
type DiscussionReader interface {
	Discussion(ctx context.Context, workspaceID, issueID string) ([]string, error)
}
