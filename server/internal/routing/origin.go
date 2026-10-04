package routing

import "strings"

// Who an executor assignment belongs to (DENE-1033).
//
// The executor slot used to mean one thing however it got filled: a value is a
// value, routing keeps it. But an agent that creates a ticket cannot see which
// model each seat runs or what tier it is, so a seat it names is a guess, and
// a guess kept as an instruction is how a mid-tier job landed on the strongest
// seat while that seat was short of quota. The source records whose decision
// the value is, and only two kinds of decision survive routing:
//
//   - a person's: picked in the app, or by an automation a person configured;
//   - a person's spoken word that an agent carried, when the server has found
//     that word in the message that started the agent's run.
//
// An agent's own choice is not read as a suggestion either. Routing decides
// from nothing and does not say who the agent had in mind.
const (
	// SourceHuman — a member picked the executor themself.
	SourceHuman = "human"
	// SourceAutomation — an autopilot a member configured picked it.
	SourceAutomation = "automation"
	// SourceQuote — an agent assigned it on the word of the person whose
	// message started its run, and the server checked that word.
	SourceQuote = "quote"
	// SourceAgent — an agent chose it on its own. Routing does not use it.
	SourceAgent = "agent"
	// SourceQuoteRejected — an agent supplied a quote that the server could
	// not verify. The slot stays empty and routing must not replace the
	// rejected choice with a different seat.
	SourceQuoteRejected = "quote_rejected"
	// SourceRouter — the routing module filled the slot.
	SourceRouter = "router"
)

// NoticeAgentPickIgnored is the one sentence a routing comment says when an
// agent named an executor that was not honoured. It never says who was named.
const NoticeAgentPickIgnored = "有 Agent 给这张票指定过执行人，没有附上和它对话的那个人的原话，所以没有采用，执行席由路由从零判断。"

// NoticeAgentTierLabelIgnored is the same for a tier label an agent attached.
const NoticeAgentTierLabelIgnored = "票上有 Agent 贴的档位标签，没有采用，执行席由路由判断。"

// QuoteHolds reports whether quote is a passage of message, word for word,
// that names the agent. Whitespace runs count as one space and case is
// ignored, so a quote copied out of a rendered message still matches; nothing
// else is forgiven. It is the whole test for "the person really said this":
// the caller has already established WHICH message and WHO wrote it.
func QuoteHolds(message, quote, agentName string) bool {
	quote = collapseSpace(quote)
	name := collapseSpace(agentName)
	if quote == "" || name == "" {
		return false
	}
	return strings.Contains(strings.ToLower(collapseSpace(message)), strings.ToLower(quote)) &&
		strings.Contains(strings.ToLower(quote), strings.ToLower(name))
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// HumanLabels is the ticket's labels minus the ones an agent attached. Tier
// labels are read from here only.
func (i Issue) HumanLabels() []string {
	if len(i.AgentLabels) == 0 {
		return i.Labels
	}
	skip := make(map[string]bool, len(i.AgentLabels))
	for _, l := range i.AgentLabels {
		skip[l] = true
	}
	var out []string
	for _, l := range i.Labels {
		if !skip[l] {
			out = append(out, l)
		}
	}
	return out
}

// IgnoredAgentPick reports that an agent named an executor and the server
// dropped the name: source "agent" with nobody in the slot.
func (i Issue) IgnoredAgentPick() bool {
	return i.AssigneeSource == SourceAgent && i.AssigneeType == ""
}

// RejectedQuote reports that an agent tried to carry a person's words but the
// server could not verify them. This is deliberately distinct from an agent's
// unquoted guess: a failed proof must wait for the agent to ask the person,
// rather than silently assigning somebody else.
func (i Issue) RejectedQuote() bool {
	return i.AssigneeSource == SourceQuoteRejected && i.AssigneeType == ""
}

// ReasonAgentReassignInFlight is what an agent hears when it tries to put a
// different executor on a ticket that is already past todo while routing is
// on (DENE-1201). The executor of a ticket in flight is not the agent's to
// change: the ways out are escalating, or closing blocked so routing advises.
const ReasonAgentReassignInFlight = "an agent cannot change the executor of an issue that is already in progress or in review while routing is on; " +
	"if the work is too hard run `multica issue escalate <id> --reason \"...\"`, " +
	"if someone else should take it run `multica issue close <id> --outcome blocked --evidence-file <path> ...` so routing can advise, " +
	"or pass --per-quote with the person's own words naming the new assignee"
