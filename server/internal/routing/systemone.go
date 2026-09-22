package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// This file is the second shape a judge call can take.
//
// The judge was born on an OpenAI-compatible chat endpoint: ask a model for
// JSON, parse the JSON, read a confidence the model wrote about itself. That
// works, but the confidence in it is prose — a number the model chose to type.
//
// TypeSafe's System One models (Jev) answer the same questions natively: a
// bounded choice plus the probability distribution behind it. The confidence
// this package gates on is then a measured property of the answer rather than
// a self-report, which is exactly what the "if function" framing wants. The
// endpoint is NOT OpenAI-shaped — one POST /v1/systemone carrying a state and
// a map of typed questions — so it cannot be reached through pkg/llm, and a
// workspace that pasted https://api.typesafe.ai into the routing settings got
// a 404 from the chat path with no way to tell why.
//
// Everything outside the request shape is unchanged: the same three branches,
// the same threshold, the same breaker, and no write when the answer is not
// clear.

// SystemOneBaseURL is the public TypeSafe endpoint host. It is used only to
// recognise a target, never as a default: a workspace that has not filled in
// an endpoint keeps using the deployment gateway.
const SystemOneBaseURL = "https://api.typesafe.ai"

// systemOneHost is the host suffix that identifies a System One endpoint.
const systemOneHost = "typesafe.ai"

// SystemOne reports whether this target speaks the System One protocol rather
// than the OpenAI chat protocol.
//
// The signal is the endpoint host, not the model id. A model name is a string
// a person types and can be anything; the host is the thing that actually
// determines which wire format the other end will accept. The deployment
// gateway is never treated as System One — it is configured by the operator
// through MULTICA_LLM_*, which is an OpenAI-compatible contract by definition.
func (t Target) SystemOne() bool {
	if !t.Override() {
		return false
	}
	return isSystemOneURL(t.BaseURL)
}

// IsSystemOneEndpoint reports whether a base URL points at a System One
// endpoint. Exported for the health payload, which names the protocol so the
// settings section can say WHICH kind of model is on the other end — "a host"
// does not tell a reader whether their tickets are being judged by Jev.
func IsSystemOneEndpoint(raw string) bool { return isSystemOneURL(raw) }

func isSystemOneURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == systemOneHost || strings.HasSuffix(host, "."+systemOneHost)
}

// systemOneEndpoint turns whatever the person pasted into the evaluation URL.
//
// People paste the host, the host with a trailing slash, the `/v1` root they
// are used to from OpenAI-compatible providers, and occasionally the full
// endpoint from the docs. All four mean the same thing here, and a 404 caused
// by a trailing `/v1` would be indistinguishable in the settings section from
// a revoked key.
func systemOneEndpoint(raw string) string {
	base := strings.TrimSpace(raw)
	if base == "" {
		base = SystemOneBaseURL
	}
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	base = strings.TrimRight(base, "/")
	base = strings.TrimSuffix(base, "/v1/systemone")
	base = strings.TrimSuffix(base, "/systemone")
	base = strings.TrimSuffix(base, "/v1")
	return base + "/v1/systemone"
}

func systemOneModelsEndpoint(raw string) string {
	return strings.TrimSuffix(systemOneEndpoint(raw), "/systemone") + "/models"
}

// systemOneTimeout bounds one evaluation. Route runs on the issue write path,
// so a hung judge is a hung ticket write.
const systemOneTimeout = 30 * time.Second

// SystemOneJudge answers the routing questions on a TypeSafe System One model.
//
// It is stateless: the endpoint and credential come from the Target on every
// call, exactly as they do for the chat path, so a rotated key is picked up on
// the next ticket instead of living in a cached client.
type SystemOneJudge struct {
	// HTTP is the client used for the evaluation call. Nil means a default
	// client with systemOneTimeout.
	HTTP *http.Client
}

// Available reports whether this judge has anywhere to send a request. A
// System One target carries both halves by construction — Target.SystemOne is
// false without them — so the only unavailable case is a target that is not
// System One at all.
func (j SystemOneJudge) Available(t Target) bool { return t.SystemOne() }

// question is one typed question in the request.
type systemOneQuestion struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type systemOneRequest struct {
	State     any                          `json:"state"`
	Model     string                       `json:"model"`
	Questions map[string]systemOneQuestion `json:"questions"`
}

// systemOneAnswer covers all three answer types. Only the fields a routing
// question asks for are read; the rest are ignored rather than rejected, so a
// future answer field cannot break a running deployment.
type systemOneAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Noul          float64            `json:"noul"`
	Score         float64            `json:"score"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

type systemOneResponse struct {
	Model   string                     `json:"model"`
	Answers map[string]systemOneAnswer `json:"answers"`
}

// Question ids. They are keys for this code only — the docs are explicit that
// the id is not shown to the model — so the meaning lives entirely in the
// instructions below.
const (
	qExecutorTier = "executor_tier"
	qReviewer     = "reviewer"
	qReviewerTier = "reviewer_tier"
	qCause        = "cause"
	qSuggested    = "suggested_tier"
	qStaleAction  = "stale_action"
)

const (
	executorInstruction = "Which seat tier should DO the work on this ticket? Pick the weakest tier that can be expected to finish it correctly without supervision; a ticket that is ambiguous, cross-cutting, or expensive to get wrong belongs on a stronger tier."
	reviewerInstruction = "Does this ticket need a separate acceptance pass after the work is done, and if so by whom?"
	reviewerTierInstr   = "Assuming another AI seat checks this work, which tier should check it? Reviewing is a judgement task: it is normally at least as demanding as doing the work."
	causeInstruction    = "This ticket is blocked. What is the most likely reason it cannot move?"
	suggestedInstr      = "If the assigned seat is not strong enough, which tier should take this ticket instead?"
	staleInstruction    = "This ticket has been sitting in review with nothing happening on it. Looking ONLY at review_remarks — what the reviewer themselves said on the ticket — has the reviewer already accepted this work?"
)

// tierCriteria describes each rung for the model. The ladder itself carries
// only a key and a Chinese label, which is an identifier rather than a rubric;
// a Choice answer is only as good as the descriptions of its options.
var tierCriteria = map[string]string{
	"strongest": "The strongest seat available. Use for work that is ambiguous, architectural, security- or money-sensitive, or hard to reverse.",
	"strong":    "A strong seat. Use for ordinary feature work, non-trivial bug fixes, and anything needing judgement across more than one file.",
	"medium":    "A mid seat. Use for well-specified changes with a clear shape and a small blast radius.",
	"weak":      "The weakest seat. Use for mechanical, fully specified work: renames, copy edits, single-line fixes.",
}

func tierChoices(candidates []string) map[string]any {
	criteria := make(map[string]any, len(candidates))
	for _, key := range candidates {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if desc, ok := tierCriteria[key]; ok {
			criteria[key] = desc
			continue
		}
		// An unknown rung is still a legal answer; the ladder is data and can
		// grow a rung this code has never heard of. Sending it with a null
		// rubric is better than silently narrowing the candidate set.
		criteria[key] = nil
	}
	return criteria
}

// Assign asks the todo-row question.
func (j SystemOneJudge) Assign(ctx context.Context, target Target, st JudgeState) (Verdict, error) {
	choices := tierChoices(st.Candidates)
	if len(choices) == 0 {
		return Verdict{}, fmt.Errorf("%w: no candidate tiers to choose from", ErrJudgeUnavailable)
	}
	req := systemOneRequest{
		State: st,
		Model: target.Model,
		Questions: map[string]systemOneQuestion{
			qExecutorTier: {Type: "choice", Instructions: executorInstruction, Criteria: choices},
			qReviewer: {Type: "choice", Instructions: reviewerInstruction, Criteria: map[string]any{
				string(ReviewerSeat):  "Another AI seat should check the work before it is accepted.",
				string(ReviewerHuman): "Acceptance needs a conversation with a person: product judgement, money, irreversible or outward-facing effects.",
				string(ReviewerNone):  "Small, self-evident work that needs no separate acceptance pass.",
			}},
			qReviewerTier: {Type: "choice", Instructions: reviewerTierInstr, Criteria: choices},
		},
	}

	resp, err := j.evaluate(ctx, target, req)
	if err != nil {
		return Verdict{}, err
	}

	executor, ok := resp.Answers[qExecutorTier]
	if !ok || executor.Choice == "" {
		return Verdict{}, fmt.Errorf("%w: no executor answer in the reply", ErrJudgeUnavailable)
	}
	reviewer, ok := resp.Answers[qReviewer]
	if !ok {
		return Verdict{}, fmt.Errorf("%w: no reviewer answer in the reply", ErrJudgeUnavailable)
	}
	kind := ReviewerKind(strings.ToLower(strings.TrimSpace(reviewer.Choice)))
	switch kind {
	case ReviewerSeat, ReviewerHuman, ReviewerNone:
	default:
		// Same rule as the chat path: an unrecognised branch is a broken
		// contract, not a low-confidence answer.
		return Verdict{}, fmt.Errorf("%w: unknown reviewer branch %q", ErrJudgeUnavailable, reviewer.Choice)
	}

	v := Verdict{
		ExecutorTier:       executor.Choice,
		ExecutorConfidence: executor.Confidence,
		Reviewer:           kind,
		ReviewerConfidence: reviewer.Confidence,
	}
	if kind == ReviewerSeat {
		tier, ok := resp.Answers[qReviewerTier]
		if !ok || tier.Choice == "" {
			return Verdict{}, fmt.Errorf("%w: reviewer is a seat but no tier was returned", ErrJudgeUnavailable)
		}
		v.ReviewerTier = tier.Choice
		// Both halves gate the one slot that gets written. Being sure the
		// ticket needs a seat reviewer says nothing about being sure WHICH
		// seat, and the slot carries the tier, so the weaker of the two is the
		// honest confidence for writing it.
		if tier.Confidence < v.ReviewerConfidence {
			v.ReviewerConfidence = tier.Confidence
		}
	}
	v.Reason = systemOneReason(resp.Model, v)
	return v, nil
}

// Unblock asks the blocked-row question. It writes nothing, so it is the one
// call whose confidence is not gated — the answer only becomes a comment.
func (j SystemOneJudge) Unblock(ctx context.Context, target Target, st JudgeState) (Advice, error) {
	choices := tierChoices(st.Candidates)
	questions := map[string]systemOneQuestion{
		qCause: {Type: "choice", Instructions: causeInstruction, Criteria: map[string]any{
			"tier":  "The seat holding it is probably not strong enough for the work.",
			"human": "A person has to make a call — a decision, an approval, or information only a person has.",
			"other": "Something else: a missing dependency, an external system, or an unclear requirement.",
		}},
	}
	if len(choices) > 0 {
		questions[qSuggested] = systemOneQuestion{Type: "choice", Instructions: suggestedInstr, Criteria: choices}
	}

	resp, err := j.evaluate(ctx, target, systemOneRequest{State: st, Model: target.Model, Questions: questions})
	if err != nil {
		return Advice{}, err
	}
	cause, ok := resp.Answers[qCause]
	if !ok || cause.Choice == "" {
		return Advice{}, fmt.Errorf("%w: no cause answer in the reply", ErrJudgeUnavailable)
	}
	a := Advice{Cause: strings.ToLower(strings.TrimSpace(cause.Choice))}
	if a.Cause == "tier" {
		a.SuggestedTier = resp.Answers[qSuggested].Choice
	}
	a.Reason = systemOneAdviceReason(resp.Model, a, cause.Confidence)
	return a, nil
}

// Stale asks the stale-review question. The branch set is two values, and the
// question is deliberately about what the reviewer ALREADY said rather than
// about the work: a model that could answer "the work is done" would be
// writing status from its own opinion.
func (j SystemOneJudge) Stale(ctx context.Context, target Target, st StaleState) (StaleDecision, error) {
	req := systemOneRequest{
		State: st,
		Model: target.Model,
		Questions: map[string]systemOneQuestion{
			qStaleAction: {Type: "choice", Instructions: staleInstruction, Criteria: map[string]any{
				string(StaleComplete): "The reviewer has explicitly passed this work in their own remarks on the ticket; only the status never followed.",
				string(StaleWake):     "Everything else: the reviewer never spoke, asked for changes, asked a question, or the remarks are unclear.",
			}},
		},
	}
	resp, err := j.evaluate(ctx, target, req)
	if err != nil {
		return StaleDecision{}, err
	}
	answer, ok := resp.Answers[qStaleAction]
	if !ok || answer.Choice == "" {
		return StaleDecision{}, fmt.Errorf("%w: no stale answer in the reply", ErrJudgeUnavailable)
	}
	action := StaleAction(strings.ToLower(strings.TrimSpace(answer.Choice)))
	switch action {
	case StaleWake, StaleComplete:
	default:
		return StaleDecision{}, fmt.Errorf("%w: unknown stale action %q", ErrJudgeUnavailable, answer.Choice)
	}
	return StaleDecision{
		Action:     action,
		Confidence: answer.Confidence,
		Reason:     modelLabel(resp.Model) + "判为「" + string(action) + "」，置信度 " + percent(answer.Confidence),
	}, nil
}

// systemOneReason writes the sentence a person reads in the decision comment.
//
// A System One model returns judgements, not prose — there is no reason field
// to read — so the sentence is composed here from the branches it chose and
// the probabilities behind them. That is a feature rather than a workaround:
// the reason a reader sees is now derived from the same numbers the code
// gated on, and cannot flatter a decision the numbers do not support.
func systemOneReason(model string, v Verdict) string {
	var b strings.Builder
	b.WriteString(modelLabel(model))
	b.WriteString(" chose the ")
	b.WriteString(v.ExecutorTier)
	b.WriteString(" tier (")
	b.WriteString(percent(v.ExecutorConfidence))
	b.WriteString(" confident)")
	switch v.Reviewer {
	case ReviewerSeat:
		b.WriteString(", and acceptance by the ")
		b.WriteString(v.ReviewerTier)
		b.WriteString(" tier (")
		b.WriteString(percent(v.ReviewerConfidence))
		b.WriteString(")")
	case ReviewerHuman:
		b.WriteString(", and acceptance by a person (")
		b.WriteString(percent(v.ReviewerConfidence))
		b.WriteString(")")
	case ReviewerNone:
		b.WriteString(", and no separate acceptance pass (")
		b.WriteString(percent(v.ReviewerConfidence))
		b.WriteString(")")
	}
	b.WriteString(".")
	return b.String()
}

// systemOneAdviceReason is the blocked-row equivalent: one sentence of
// judgement and one of suggestion, composed from the chosen branch.
func systemOneAdviceReason(model string, a Advice, confidence float64) string {
	var b strings.Builder
	b.WriteString(modelLabel(model))
	switch a.Cause {
	case "tier":
		b.WriteString(" reads this as the seat being out of its depth (")
		b.WriteString(percent(confidence))
		b.WriteString(" confident).")
		if a.SuggestedTier != "" {
			b.WriteString(" Try the ")
			b.WriteString(a.SuggestedTier)
			b.WriteString(" tier instead.")
		}
	case "human":
		b.WriteString(" reads this as needing a person to decide (")
		b.WriteString(percent(confidence))
		b.WriteString(" confident). Reassigning it to another seat is unlikely to move it.")
	default:
		b.WriteString(" could not attribute this to the seat or to a pending decision (")
		b.WriteString(percent(confidence))
		b.WriteString(" confident on \"other\"). The blocker is probably outside this ticket.")
	}
	return b.String()
}

func modelLabel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "The routing model"
	}
	return model
}

// percent renders a probability the way a reader can act on it. One decimal
// would imply a precision the model does not claim.
func percent(p float64) string {
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	return itoa(int(p*100+0.5)) + "%"
}

func (j SystemOneJudge) evaluate(ctx context.Context, target Target, body systemOneRequest) (systemOneResponse, error) {
	if strings.TrimSpace(target.Model) == "" {
		return systemOneResponse{}, fmt.Errorf("%w: no model selected", ErrJudgeUnavailable)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return systemOneResponse{}, fmt.Errorf("%w: %v", ErrJudgeUnavailable, err)
	}
	endpoint := systemOneEndpoint(target.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return systemOneResponse{}, fmt.Errorf("%w: %v", ErrJudgeUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(target.APIKey))

	client := j.HTTP
	if client == nil {
		client = &http.Client{Timeout: systemOneTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return systemOneResponse{}, fmt.Errorf("%w: %v", ErrJudgeUnavailable, err)
	}
	defer resp.Body.Close()

	// Bounded read: the reply is a handful of answers, and an endpoint that
	// streams megabytes at the issue write path is a fault, not content.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return systemOneResponse{}, fmt.Errorf("%w: %v", ErrJudgeUnavailable, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Wrapped, never formatted in: the breaker classifies 401/402/403/429
		// with errors.As, and flattening the status to text disables that.
		return systemOneResponse{}, fmt.Errorf("%w: %w", ErrJudgeUnavailable, &FatalStatusError{
			Code: resp.StatusCode,
			Err:  fmt.Errorf("the routing model returned %d: %s", resp.StatusCode, clip(string(raw), 200)),
		})
	}
	var out systemOneResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return systemOneResponse{}, fmt.Errorf("%w: reply was not JSON: %v", ErrJudgeUnavailable, err)
	}
	if len(out.Answers) == 0 {
		return systemOneResponse{}, fmt.Errorf("%w: reply carried no answers", ErrJudgeUnavailable)
	}
	return out, nil
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ListSystemOneModels reads the model names a System One account may use.
//
// It exists because the endpoint's list is NOT the OpenAI `{"data":[{"id"}]}`
// shape that pkg/llm parses, which is the whole reason a workspace pointed at
// TypeSafe saw an empty model list and concluded the product did not support
// Jev at all.
func ListSystemOneModels(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, systemOneModelsEndpoint(baseURL), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	if client == nil {
		client = &http.Client{Timeout: systemOneTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &FatalStatusError{Code: resp.StatusCode, Err: fmt.Errorf("model list returned %d", resp.StatusCode)}
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("model list was not JSON: %w", err)
	}
	names := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		if name := strings.TrimSpace(m.Name); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// ProviderJudge sends each call to the judge that speaks the target's
// protocol.
//
// It is a dispatcher and nothing else: both judges answer the same questions
// with the same types, the threshold and the breaker sit above it, and adding
// a third protocol is a case here rather than a change anywhere in Route.
type ProviderJudge struct {
	// Chat is the OpenAI-compatible judge. It handles the deployment gateway
	// and every workspace endpoint that is not System One.
	Chat Judge
	// SystemOne handles TypeSafe System One endpoints.
	SystemOne Judge
}

func (p ProviderJudge) pick(t Target) Judge {
	if t.SystemOne() && p.SystemOne != nil {
		return p.SystemOne
	}
	return p.Chat
}

func (p ProviderJudge) Assign(ctx context.Context, t Target, st JudgeState) (Verdict, error) {
	j := p.pick(t)
	if j == nil {
		return Verdict{}, ErrJudgeUnavailable
	}
	return j.Assign(ctx, t, st)
}

func (p ProviderJudge) Stale(ctx context.Context, t Target, st StaleState) (StaleDecision, error) {
	j := p.pick(t)
	if j == nil {
		return StaleDecision{}, ErrJudgeUnavailable
	}
	return j.Stale(ctx, t, st)
}

func (p ProviderJudge) Unblock(ctx context.Context, t Target, st JudgeState) (Advice, error) {
	j := p.pick(t)
	if j == nil {
		return Advice{}, ErrJudgeUnavailable
	}
	return j.Unblock(ctx, t, st)
}

// Available delegates to whichever judge would take the call, so the settings
// section reports on the endpoint the workspace actually configured.
func (p ProviderJudge) Available(t Target) bool {
	j := p.pick(t)
	if j == nil {
		return false
	}
	if a, ok := j.(Availability); ok {
		return a.Available(t)
	}
	return true
}
