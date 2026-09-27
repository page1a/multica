package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// SummaryFacts is what the parking record (DENE-881) hands the routing model
// when the agent left no words of its own, or only an error. The category is
// already decided; the model only phrases it.
type SummaryFacts struct {
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	Category  string   `json:"category"`
	Reasons   []string `json:"reasons,omitempty"`
	AgentSaid string   `json:"agent_said,omitempty"`
	RunError  string   `json:"run_error,omitempty"`
}

// Summarizer is the optional half of Judge that writes one sentence.
type Summarizer interface {
	Summarize(ctx context.Context, t Target, f SummaryFacts) (string, error)
}

const summaryTimeout = 20 * time.Second

const summarySystemPrompt = `You write one short sentence in Simplified Chinese for a busy person scanning a list of work tickets.

The payload says why a ticket stopped: category is already decided and must not be changed, reasons are the facts, agent_said and run_error are raw text from the run. Say plainly what happened before it stopped and what is stuck, in at most 40 Chinese characters. No markdown, no ticket ids, no advice, no speculation beyond the payload.

Respond with a JSON object: {"sentence": "..."}.`

// Summarize asks the workspace's routing model for one sentence. Routing
// switched off, the breaker cooling down, or a judge that cannot write prose
// all return ErrJudgeUnavailable, and the caller uses its fixed wording. It
// never trips the breaker: a failed summary must not take routing down.
func (r *Router) Summarize(ctx context.Context, workspaceID string, f SummaryFacts) (string, error) {
	if r == nil || r.Store == nil {
		return "", ErrJudgeUnavailable
	}
	settings, err := r.Store.Settings(ctx, workspaceID)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrJudgeUnavailable, err)
	}
	if !settings.State().Active() {
		return "", ErrJudgeUnavailable
	}
	if open, _, _ := r.Breaker.Open(workspaceID); open {
		return "", ErrJudgeUnavailable
	}
	s, ok := r.Judge.(Summarizer)
	if !ok {
		return "", ErrJudgeUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, summaryTimeout)
	defer cancel()
	return s.Summarize(ctx, settings.Target(), f)
}

// Summarize dispatches to the judge that speaks the target's protocol.
func (p ProviderJudge) Summarize(ctx context.Context, t Target, f SummaryFacts) (string, error) {
	s, ok := p.pick(t).(Summarizer)
	if !ok {
		return "", ErrJudgeUnavailable
	}
	return s.Summarize(ctx, t, f)
}

// Summarize writes the sentence on the OpenAI-compatible path.
func (j LLMJudge) Summarize(ctx context.Context, t Target, f SummaryFacts) (string, error) {
	raw, err := j.ask(ctx, t, summarySystemPrompt, f)
	if err != nil {
		return "", err
	}
	var out struct {
		Sentence string `json:"sentence"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return "", fmt.Errorf("%w: %v", ErrJudgeUnavailable, err)
	}
	sentence := strings.Join(strings.Fields(out.Sentence), " ")
	if sentence == "" {
		return "", ErrJudgeUnavailable
	}
	if utf8.RuneCountInString(sentence) > 80 {
		sentence = string([]rune(sentence)[:80]) + "…"
	}
	return sentence, nil
}
