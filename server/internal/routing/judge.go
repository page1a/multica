package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ReviewerKind is what the judge decided about acceptance for this issue.
type ReviewerKind string

const (
	// ReviewerSeat — an agent from the ladder should check the work.
	ReviewerSeat ReviewerKind = "seat"
	// ReviewerHuman — this acceptance needs a person, because it needs a
	// conversation rather than a check.
	//
	// It never becomes the reviewer slot's VALUE. A slot naming a person
	// hands the ticket to that person at 待验收, and from then on routing
	// skips it — an issue a person holds is that person's issue — so the
	// ticket freezes with nobody able to move it on. The answer is kept as a
	// note instead: a seat takes the slot and is told to @ the person for the
	// call it cannot make.
	ReviewerHuman ReviewerKind = "human"
	// ReviewerNone — this issue does not need a separate acceptance pass.
	//
	// This is a value, not an absence. An empty reviewer slot is re-judged on
	// every status change forever, so "no review needed" has to be written
	// down for the fill-only-empty-slots rule to ever close.
	ReviewerNone ReviewerKind = "none"
)

// Verdict is the judge's whole answer for a todo issue: a branch plus a
// confidence, and nothing else. No prose the product depends on, no action, no
// status.
type Verdict struct {
	// ExecutorTier is a tier key from the ladder.
	ExecutorTier string `json:"executor_tier"`
	// ExecutorConfidence gates the executor slot alone.
	ExecutorConfidence float64 `json:"executor_confidence"`
	// Reviewer is one of seat / human / none.
	Reviewer ReviewerKind `json:"reviewer"`
	// ReviewerTier is a tier key, meaningful only when Reviewer is seat.
	ReviewerTier string `json:"reviewer_tier"`
	// ReviewerConfidence gates the reviewer slot alone. The two slots are
	// gated separately on purpose: being sure who should do the work says
	// nothing about being sure who should check it, and one shared gate would
	// make a confident half fail with an unconfident one.
	ReviewerConfidence float64 `json:"reviewer_confidence"`
	// Reason is shown to a human in the decision comment. It is never parsed.
	Reason string `json:"reason"`
}

// Advice is the judge's answer for a blocked issue. It carries no slot values
// at all, because the blocked row of the state table writes nothing.
type Advice struct {
	// Cause is one of "tier" (the seat was not strong enough), "human" (needs
	// a person to decide), or "other".
	Cause string `json:"cause"`
	// SuggestedTier is a tier key when Cause is "tier".
	SuggestedTier string `json:"suggested_tier"`
	// Reason is one sentence of judgement and one of suggestion.
	Reason string `json:"reason"`
}

// State is the trimmed, structured view of an issue the judge is given.
//
// It is deliberately small. Seat headroom is absent because Multica already
// queues a full seat, so feeding a field that does not participate in the
// decision is noise. Credentials and full issue bodies are absent because this
// payload leaves the deployment.
type JudgeState struct {
	Title              string   `json:"title"`
	DescriptionSummary string   `json:"description_summary"`
	Labels             []string `json:"labels,omitempty"`
	Project            string   `json:"project,omitempty"`
	Repository         string   `json:"repository,omitempty"`
	Status             string   `json:"status"`
	ParentExecutor     string   `json:"parent_executor,omitempty"`
	HasChildren        bool     `json:"has_children"`
	Direction          string   `json:"direction,omitempty"`
	Candidates         []string `json:"candidate_tiers"`
	// RoutingPolicy and PolicyPrompt are the tier preference. Omitted on
	// questions that do not choose a tier, so a stale-review request does
	// not grow a blank policy object.
	RoutingPolicy RoutingPolicy `json:"routing_policy,omitempty"`
	PolicyPrompt  string        `json:"policy_prompt,omitempty"`
	// Seats describes every candidate, including ones filtered out of
	// candidate_tiers, so the request shows why a rung is missing. Latency
	// and quota here are null when unobserved.
	Seats []SeatSnapshot `json:"seats,omitempty"`
	// ProviderQuotas is the subscribed provider rollup. It is not a seat's
	// liveness: a missing provider row is unknown and does not remove seats.
	ProviderQuotas []ProviderQuota `json:"provider_quotas,omitempty"`
}

// Judge answers the two questions Route cannot answer deterministically.
// Implementations must not write anything anywhere.
type Judge interface {
	Assign(ctx context.Context, target Target, st JudgeState) (Verdict, error)
	Unblock(ctx context.Context, target Target, st JudgeState) (Advice, error)
	// Stale answers the stale-review row: wake the reviewer, or align a
	// status to an acceptance the ticket already carries. It is the only
	// question whose answer can reach a status write, which is why its
	// branch set is two values and why the caller gates it a second time.
	Stale(ctx context.Context, target Target, st StaleState) (StaleDecision, error)
}

// TextGenerator is the slice of the server-internal LLM layer this package
// uses. pkg/llm.Client satisfies it.
type TextGenerator interface {
	GenerateJSON(ctx context.Context, model, systemPrompt, userPrompt string, temperature float64, maxCompletionTokens int64) (string, error)
}

// LLMJudge runs the judge on the server-internal LLM layer.
//
// It deliberately does not shell out to the operator's local `jev` binary:
// that binary runs on a personal machine and the server cannot reach it. What
// is reused is the discipline — a bounded branch set, one threshold living in
// one place, and no write when the answer is not clear — not the program.
type LLMJudge struct {
	// Gen is the deployment's own generator (MULTICA_LLM_*). It serves every
	// workspace that did not bring its own gateway, which is all of them until
	// somebody fills the endpoint pair in.
	Gen TextGenerator
	// Dial builds a generator for a workspace-owned gateway. Nil means this
	// build cannot honour a workspace endpoint at all, and such a target falls
	// back to Gen rather than failing — an unwired factory is a deployment
	// fact, not a reason to stop routing.
	//
	// It is a factory rather than a cache on purpose: one call per routing
	// pass is cheap next to the model round-trip it is about to make, while a
	// cache keyed by credential would keep a rotated-away key alive in memory
	// for as long as the process runs.
	Dial func(baseURL, apiKey string) TextGenerator
}

// generator picks the endpoint one call goes to.
func (j LLMJudge) generator(t Target) TextGenerator {
	if t.Override() && j.Dial != nil {
		return j.Dial(t.BaseURL, t.APIKey)
	}
	return j.Gen
}

// Availability is the optional half of Judge: an implementation that can say,
// without making a call, that it has nothing to call at all.
//
// Health uses it so a deployment that never configured a server-internal LLM
// is reported the moment somebody opens the settings section, instead of only
// after the first ticket has failed against it.
type Availability interface {
	Available(target Target) bool
}

// Available reports whether the generator behind this judge has anywhere to
// send a request.
//
// A generator that cannot answer the question is treated as available: the
// only honest reading of "unknown" is to let the real call decide, and
// claiming a fault on a guess is exactly the mistake this whole surface is
// supposed to avoid.
func (j LLMJudge) Available(t Target) bool {
	// A workspace that supplied both halves has somewhere to send a request by
	// construction, whatever the deployment did or did not configure. Reading
	// the deployment client here is what used to report "this deployment has
	// no internal LLM" at a workspace that had just typed in its own.
	if t.Override() && j.Dial != nil {
		return true
	}
	type enabler interface{ Enabled() bool }
	if e, ok := j.Gen.(enabler); ok {
		return e.Enabled()
	}
	return true
}

// NotConfiguredReason is the wording shown when neither the workspace nor the
// deployment has an endpoint to call. Named once so Health and the breaker
// cannot drift apart on it.
const NotConfiguredReason = "no routing endpoint is configured — set one for this workspace, or configure the deployment's internal LLM"

// ErrJudgeUnavailable reports that no answer could be obtained. Route turns it
// into the else branch; it never becomes a partial write.
var ErrJudgeUnavailable = errors.New("routing: judge unavailable")

const assignSystemPrompt = `You route work tickets to seats on a fixed ladder of AI agents.

Answer three things and nothing else:
1. executor_tier: which ladder tier should DO this work. Choose from candidate_tiers exactly.
2. reviewer: whether this ticket needs a separate acceptance pass — "seat" (an agent checks it), "human" (acceptance needs a conversation with a person, e.g. product judgement, money, irreversible or outward-facing effects), or "none" (small, self-evident work).
3. reviewer_tier: when reviewer is "seat", which tier checks it. Choose from candidate_tiers exactly.

Large or vague tickets default to needing review. Report calibrated confidence in [0,1] separately for the executor choice and the reviewer choice; below-threshold answers are discarded rather than used, so do not inflate them.

Respond with a JSON object with keys: executor_tier, executor_confidence, reviewer, reviewer_tier, reviewer_confidence, reason. reason is one short sentence for a human reader.

Follow policy_prompt in the user payload when choosing executor_tier. That text is the preference. You still only return the JSON object above: you do not change status, assignee, or any other ticket field, and you do not take an action. A fact marked unknown is missing: do not treat it as zero, available, or exhausted.`

const unblockSystemPrompt = `A work ticket is blocked. Say only what is likely wrong.

cause: "tier" if the assigned seat is probably not strong enough, "human" if a person has to make a call, "other" otherwise.
suggested_tier: when cause is "tier", which tier to try instead, chosen from candidate_tiers exactly.
reason: one sentence of judgement and one of suggestion.

You are not changing anything on the ticket. Follow policy_prompt in the user payload if you suggest a tier. Respond with a JSON object with keys: cause, suggested_tier, reason.`

// Assign asks the todo-row question. Any transport, decoding, or contract
// failure returns an error wrapping ErrJudgeUnavailable; the caller must not
// be able to mistake a broken call for a low-confidence answer.
func (j LLMJudge) Assign(ctx context.Context, target Target, st JudgeState) (Verdict, error) {
	raw, err := j.ask(ctx, target, assignSystemPrompt, st)
	if err != nil {
		return Verdict{}, err
	}
	var v Verdict
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return Verdict{}, fmt.Errorf("%w: verdict was not JSON: %v", ErrJudgeUnavailable, err)
	}
	v.Reviewer = ReviewerKind(strings.ToLower(strings.TrimSpace(string(v.Reviewer))))
	switch v.Reviewer {
	case ReviewerSeat, ReviewerHuman, ReviewerNone:
	default:
		// An unrecognised branch is not a low-confidence answer, it is a
		// broken contract. Treating it as one would write a slot from a reply
		// this code does not understand.
		return Verdict{}, fmt.Errorf("%w: unknown reviewer branch %q", ErrJudgeUnavailable, v.Reviewer)
	}
	return v, nil
}

// Unblock asks the blocked-row question.
func (j LLMJudge) Unblock(ctx context.Context, target Target, st JudgeState) (Advice, error) {
	raw, err := j.ask(ctx, target, unblockSystemPrompt, st)
	if err != nil {
		return Advice{}, err
	}
	var a Advice
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return Advice{}, fmt.Errorf("%w: advice was not JSON: %v", ErrJudgeUnavailable, err)
	}
	return a, nil
}

// ask sends one question and returns the raw JSON body. st is the question's
// own state struct: every row embeds JudgeState and adds its own fields, which
// is why this takes any rather than the base type.
func (j LLMJudge) ask(ctx context.Context, target Target, system string, st any) (string, error) {
	gen := j.generator(target)
	if gen == nil {
		return "", ErrJudgeUnavailable
	}
	payload, err := json.Marshal(st)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrJudgeUnavailable, err)
	}
	raw, err := gen.GenerateJSON(ctx, target.Model, system, string(payload), 0, 400)
	if err != nil {
		// Both errors are wrapped, not formatted in: the breaker classifies
		// 401/402/403/429 out of the upstream error with errors.As, and a %v
		// here would flatten it to text and silently disable that.
		return "", fmt.Errorf("%w: %w", ErrJudgeUnavailable, err)
	}
	return raw, nil
}

// StaleAction is the judge's whole answer for the stale-review row. It is a
// closed two-value set, and that is the point: the model is asked "wake, or
// align the status to a verdict that already exists", never "is this work
// done".
type StaleAction string

const (
	// StaleWake — nobody is going to move this ticket; put it back in front of
	// the reviewer. Writes no status.
	StaleWake StaleAction = "wake"
	// StaleComplete — the reviewer already passed this on the ticket and the
	// status simply never followed. Only ever honoured when the deterministic
	// gate in routeStale agrees that the reviewer actually spoke.
	StaleComplete StaleAction = "complete"
)

// StaleDecision is one answer plus its confidence. No prose the product
// depends on, no action, no free-form status.
type StaleDecision struct {
	Action     StaleAction `json:"action"`
	Confidence float64     `json:"confidence"`
	Reason     string      `json:"reason"`
}

// StaleState is the trimmed view the stale-review question is asked against.
// It carries the reviewer's own remarks because the whole question is whether
// THOSE remarks already contain an acceptance — not whether the work looks
// finished.
type StaleState struct {
	JudgeState
	// QuietHours is how long the ticket has been sitting in the in-review
	// category with nothing happening on it.
	QuietHours int `json:"quiet_hours"`
	// Reviewer is the display name of the seat or person holding the
	// acceptance.
	Reviewer string `json:"reviewer"`
	// ReviewRemarks are what the reviewer said on this ticket, oldest first
	// and clipped. Empty means the reviewer never spoke, and an empty list can
	// never produce a completion — routeStale refuses it before the answer is
	// read.
	ReviewRemarks []string `json:"review_remarks"`
}

const staleSystemPrompt = `A work ticket has been sitting in "in review" with nothing happening on it, and no run is active. Decide one thing.

action: "complete" ONLY IF review_remarks already contain an explicit acceptance from the reviewer — they checked the work and passed it. "wake" for everything else, including an empty review_remarks, remarks that ask for changes, remarks that are questions, and remarks you are unsure about.

You are NOT judging whether the work is finished. You are judging whether the reviewer has ALREADY said it is. If nobody has said so on the ticket, the answer is "wake".

confidence: calibrated in [0,1]. A below-threshold answer is discarded and treated as "wake", so do not inflate it.

Respond with a JSON object with keys: action, confidence, reason. reason is one short sentence for a human reader.`

// Stale asks the stale-review question.
func (j LLMJudge) Stale(ctx context.Context, target Target, st StaleState) (StaleDecision, error) {
	raw, err := j.ask(ctx, target, staleSystemPrompt, st)
	if err != nil {
		return StaleDecision{}, err
	}
	var d StaleDecision
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return StaleDecision{}, fmt.Errorf("%w: stale decision was not JSON: %v", ErrJudgeUnavailable, err)
	}
	d.Action = StaleAction(strings.ToLower(strings.TrimSpace(string(d.Action))))
	switch d.Action {
	case StaleWake, StaleComplete:
	default:
		// An unrecognised branch is a broken contract, not a weak answer. The
		// caller would otherwise read the zero value, and the zero value of a
		// string is not "wake".
		return StaleDecision{}, fmt.Errorf("%w: unknown stale action %q", ErrJudgeUnavailable, d.Action)
	}
	return d, nil
}
