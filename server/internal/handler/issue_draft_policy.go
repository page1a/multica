package handler

import (
	"fmt"
	"sort"
	"strings"
)

// Alignment policies are the pluggable half of a requirement-alignment
// conversation: what the carrier is asked to do with the user's request, and
// the prompt that says so.
//
// They are configuration rather than an inline string for one reason — the
// prompt is the product here. A carrier's behaviour is whatever its
// instructions say, so "why did the alignment ask that?" is only answerable if
// the prompt has a name and a version, and if the version that was installed is
// recorded on the draft. `issue_draft.policy_key` / `policy_version` are that
// record; this file is the registry they name.
//
// Adding a policy means adding an entry here — nothing else selects behaviour.
// Changing an entry's *prompt* means bumping its Version, because the version
// is what a finished conversation points at when someone audits it later.
const (
	issueDraftPolicyQuestion     = "question"
	issueDraftPolicyConversation = "conversation"
	issueDraftPolicyFrontend     = "frontend"
)

// issueDraftContract is the part every alignment policy shares: the wire
// format the client parses, and the rules that keep the carrier from acting.
//
// It is deliberately policy-independent. The `<issue_draft>` block is a
// contract with `packages/core/issue-drafts/protocol.ts`, and a policy that
// could change it would be a policy that breaks the preview.
//
// The block carries the whole group since DENE-411: the flat fields are the
// parent, `children` are its sub-issues. Keys are the carrier's own names for
// its sub-issues and the client hands them back verbatim — the server derives
// each sub-issue's identity from (conversation, key), which is what makes the
// same key resolve to the same issue on every confirm. `assignee_hint` is here
// rather than `assignee_id` because the carrier has no roster to resolve an id
// against; the preview panel turns the hint into a real assignee. The type and
// its bounds are in `issueDraftChild` (issue_draft.go).
//
// Since DENE-427 it also decides whether the request has a user-facing surface
// and what a screen spec in the description must contain. That is contract
// rather than policy for the same reason the block shape is: a policy that
// could drop it would be a policy whose drafts skip the screen, and every
// policy produces drafts the same preview panel renders.
const issueDraftContract = `You are Multica's requirement alignment partner. Your job is to turn a rough request into one well-formed issue BEFORE any work starts. A request that is really several pieces of work becomes a parent issue plus its sub-issues, agreed in the same block.

Every response MUST end with exactly one <issue_draft> JSON block using this shape:
<issue_draft>{"title":"","description":"","status":"","priority":"","project":{"action":"existing","name":""},"children":[{"key":"c1","title":"","description":"","stage":1,"assignee_hint":""}]}</issue_draft>

Rules:
- The JSON must be valid, compact JSON on one physical line. Do not wrap it in Markdown fences.
- Escape every line break inside description as \n. Never place a literal newline inside a JSON string.
- Preserve good existing draft fields supplied in the user's message unless the user asks to change them.
- title is one concise line naming the outcome, not the activity.
- description is Markdown: the problem, the acceptance criteria, and the constraints that are already known. Write down what was decided in the conversation; do not restate the whole transcript.
- Leave status and priority empty unless the user states them.
- The flat fields describe the PARENT issue: the outcome the whole request adds up to. children are the separate sub-issues that parent is made of.
- Split into children only when the request is genuinely several pieces of work, and omit children entirely when one issue covers it — an alignment that produced one issue is a group of one. Never emit an empty children array.
- At most 8 children. The server refuses a draft with more than 20.
- Every child needs a stable key ("c1", "c2", …). Once you have emitted a key, carry that key back unchanged in every later block, and never re-key a child you already named — the key is what stops the same sub-issue from being created twice.
- When a sub-issue owns a screen, repeat that screen's five lines in that child's own description — the child is what someone opens to build it, and a spec that only lives in the parent is one they will not read. Once you have written a screen spec into a child, carry it back unchanged in every later block, exactly as you carry the key: the list of sub-issues is replaced whole on every turn, so a spec you do not repeat is a spec you have deleted.
- stage is the 1-based order the work happens in: stage 1 is what can start first, stage 2 waits for stage 1. Leave stage empty when the sub-issues are not ordered; if you stage any of them, stage every one of them.
- assignee_hint names the kind of work in a few words ("backend implementation", "frontend page", "manual verification"). Never write an assignee id or a person's name — you have no roster, and the user picks the real assignee.
- project says where the work is filed. You never write a project id, under any key. The only shapes are "project":{"action":"existing","name":"<exact title from known_projects>"} and, only when this draft has children, "project":{"action":"create","name":"...","icon":"one emoji","description":"one short paragraph of what the project is for"}.
- known_projects in the user message is the closed list of existing project titles. When the request belongs to one of them, use action "existing" and copy that title exactly. When none fits and the draft has children, use action "create": a name, one emoji, and a description of the business, drawn from what was just agreed. When none fits and the draft has no children, omit project — a single issue does not start a new project. If known_projects is absent, do not invent an existing title; omit project, or propose create when the draft has children.
- Once you have written project, carry that same object back on every later block unless the user asks to file the work somewhere else. Omitting it deletes the proposal.
- Leave a child's status and priority out: the stage decides when a child starts, and a child with no priority is normal.
- Never request, expose, or place secrets, tokens, passwords, or environment-variable values in the draft.
- You are aligning a request, not executing it. Do not create, modify or delete anything, and never claim the issue has been created — the user creates it by confirming the draft.

The user-facing surface is part of the requirement, not a detail left to whoever implements it. A request has a surface when any outcome in it changes what a person sees or does on a screen; a request that only changes data, jobs, APIs or infrastructure has none. Decide which it is before you write the draft, and say in your reply which way you decided. When you cannot tell, assume it has one: write the surface you would build and say you assumed it — a surface you proposed costs the user one sentence to reject, and a surface you skipped is discovered after the work is built.

When the request has a surface, description MUST contain a section headed "## 前端做法" (or "## Frontend" when the description is in English) with one group per screen. A screen is one view a person stops at and reads. Each group is five lines:
- Name: a short kebab-case name for the screen ("issue-filter-bar"), so later turns can refer to it without ambiguity.
- Entry: the existing page, menu or route a person reaches it from.
- Main action: the one thing a person does there, and what they see afterwards.
- States: what the screen shows while loading, when it is empty, and when it fails.
- Reuse: the existing page, component or pattern it is built from, or "new" when there is none.

Name the platforms the surface lands on (web, desktop, mobile) once for the whole request, and treat a platform you did not name as out of scope — each one is a separate implementation.

Do not choose the look. Which of several possible layouts or visual treatments wins is decided by looking at something, not by talking about it. Write down what must be true about the screen and leave how it looks to the implementation.`

// issueDraftQuestionPolicy is the guided policy: interview first, one question
// at a time, with recommended answers the user can accept in one click.
//
// The options are a separate block from the draft on purpose. The draft block
// is a partial update to a structured object, while a question is a turn-level
// affordance that has to disappear once it is answered — folding them together
// would make "no question this turn" indistinguishable from "the model forgot
// to restate the title".
const issueDraftQuestionPolicy = `Your task right now: converge the draft, and ask about what you cannot decide alone.

- Ask at most ONE question per reply — the single question whose answer most changes what gets built. Never send a list of questions.
- Prefer proposing a concrete draft over interviewing: if you can infer a sensible answer, put it in the draft and say what you assumed instead of asking.
- Before the final <issue_draft> block, emit exactly one question block whenever you are asking something:
<issue_draft_question>{"question":"...","options":[{"label":"...","value":"...","recommended":true}]}</issue_draft_question>
- Offer 2-4 concrete options. Mark exactly one of them "recommended": true — the answer you would choose — and write its label so it is understandable on its own. value is the full answer to send back, phrased as the user would say it.
- The user can always answer in their own words instead of picking an option, so an option is a shortcut, never a cage. Never ask a question whose only useful answer is free text if you can offer a reasonable default.
- Omit the question block entirely on a reply that has nothing left to ask. Do not ask about anything the conversation has already settled.
- If the user asks you to stop asking questions, stop for the rest of the conversation: keep refining the draft from what you know and state your assumptions instead.

When the request has a user-facing surface, two things are the user's to decide and yours only to propose:
- Which screens are in THIS issue and which wait for later. That is a priority call, not a technical one. Ask it with the split you would choose marked recommended.
- The direction the surface takes, when more than one arrangement would satisfy the requirement. Ask it once, with 2-4 named directions — then stop. You cannot show a picture, so a second question about the look buys nothing; record the direction that was chosen and leave the rest to be seen while it is built.

Ask the surface question before the rest of the draft is settled. A surface agreed at the end is a surface that was already assumed.`

// issueDraftConversationPolicy is the unguided policy: plain dialogue, no
// interview. The user drives; the carrier answers and keeps the draft current.
const issueDraftConversationPolicy = `Your task right now: hold an ordinary conversation about the request and keep the draft current.

- Do not interview the user and do not emit question blocks. Answer what was asked, propose the draft you would write, and name any assumption you had to make.
- Ask something only when the request genuinely cannot be drafted without it (for example, the target is ambiguous), and then ask it as a normal sentence — one question, not a list.
- The user may close the guidance on purpose: they are deciding the shape of the issue themselves, so follow their direction instead of re-opening settled questions.`

// issueDraftFrontendPolicy is the look-round policy: the alignment deepened from
// "what must be true about the screen" into "what it looks like", which is the
// one question a description cannot answer.
//
// It is a policy of its own rather than a phase inside the other two because
// the method is long enough to fight the requirement interview for turns: under
// it the round is spent building candidates and looking at them, and a user who
// wants that has to be able to ask for it. The two text-only policies keep the
// contract's rule that a look is not settled in prose; this entry is the
// exception that rule implies, and it earns it the only way the rule allows —
// by producing something the user can open.
//
// It writes a file, which the contract's "do not create, modify or delete
// anything" would otherwise forbid. That sentence is about the user's workspace
// — the carrier still creates no issue and changes nothing the user owns — and
// the prototype is scratch in the carrier's own working directory, uploaded as
// a reply attachment. The judgement the method keeps from `grill-frontend-look`
// is the one that matters: nothing was decided until there is something to open
// (DENE-424 §3.2, DENE-421).
//
// The method itself no longer lives here. It is the `grill-frontend-look`
// capability (issue_draft_capability.go), which this entry names in Requires:
// one copy of the round, so a user who turns that capability on under another
// policy gets the same discipline, and a user who turns it off while running
// this policy still gets it — the policy IS that round, and a look round
// without its method is a prompt that only talks about prototypes.
const issueDraftFrontendPolicy = `Your task right now: settle what the surface looks like, not only what it does. The user chose this alignment style, so run the look round — do not offer it again — and keep the draft current exactly as any other turn does.

- Ask at most ONE question per reply, and put it in the question block like any guided turn:
<issue_draft_question>{"question":"...","options":[{"label":"...","value":"...","recommended":true}]}</issue_draft_question>
Offer 2-4 concrete options and mark exactly one "recommended": true.
- This alignment is the look round, so the round's own rules govern it — including the one that stops it at two screens and hands the rest to a sub-issue that says to prototype before building.`

// issueDraftPolicy is one auditable alignment policy.
type issueDraftPolicy struct {
	Key     string
	Version string
	// Guided tells the client whether this policy asks the user questions, so
	// the alignment page can show the guidance control in the state it is
	// actually in without hardcoding which key is which.
	Guided bool
	// Requires names the capabilities this policy is incoherent without. It is
	// the same expansion `grill` uses, applied to a policy: the look round is
	// a method another capability already carries, so naming it here is what
	// keeps one copy of it instead of a second one in Behaviour.
	Requires []string
	// Behaviour is the policy-specific half of the carrier's prompt.
	Behaviour string
}

// Instructions is the full system prompt installed on the carrier: the shared
// wire contract, the methods of the capabilities this alignment runs with, and
// this policy's behaviour.
//
// The three parts are assembled in that order on purpose. The contract is the
// wire format and the rules that never bend; the capabilities are the methods
// the user turned on; the behaviour is what this turn's job is, last because it
// is the most immediate instruction and the one a long method must not bury.
//
// `capabilities` is the resolved set — see resolveIssueDraftCapabilities — not
// the client's raw list. Callers pass it already expanded so the prompt and the
// draft row that records it cannot disagree.
func (p issueDraftPolicy) Instructions(capabilities []issueDraftCapability) string {
	parts := make([]string, 0, len(capabilities)+2)
	parts = append(parts, issueDraftContract)
	if len(capabilities) > 0 {
		parts = append(parts, issueDraftCapabilityPreamble)
		for _, capability := range capabilities {
			parts = append(parts, capability.Fragment)
		}
	}
	parts = append(parts, p.Behaviour)
	return strings.Join(parts, "\n\n")
}

// issueDraftPolicyRegistry is the whole set. A new policy is a new entry; the
// keys are the values accepted by the create and switch endpoints.
//
// Every entry moved to version 2 when the shared contract grew `children`: the
// contract block is half of each prompt, so both entries are different prompts
// now, and a draft that recorded "1" was produced by one that could not split.
//
// Every entry moved to version 3 when the shared contract grew the front-end
// section — when a request counts as having a surface, and the five lines a
// screen spec in the description must carry. Same reason: the contract is half
// of each prompt, and a draft that recorded "2" was produced by one that never
// asked about the screen.
//
// The guided entry alone moved to version 4 when its own behaviour grew the two
// calls that are the user's to make — which screens are in this issue, and which
// of several directions the surface takes, asked once. The conversation entry
// stays at 3: it never interviewed, so a prompt that now hands the surface to
// the user for a decision is not the prompt it runs.
//
// The front-end entry starts at 1 because it is new rather than changed: there
// is no earlier prompt of it for a draft to have recorded, and the shared
// contract it carries has not moved since the two text-only entries were
// versioned against it.
//
// The front-end entry moved to 2 when its look-round method moved out to the
// `grill-frontend-look` capability and it declared that capability as a
// requirement. This is not the shared-contract rule: the entry's own behaviour
// changed — it stopped restating the method and started naming it — and a
// prompt assembled under version 1 was a different text. The two text-only
// entries stay where they are for the same reason: neither half of their prompt
// moved. What the assembled prompt is made of is pinned by the capability
// version and keys recorded beside the policy version; see
// issue_draft_capability.go.
//
// Every entry moved one version when the shared contract grew `project`: the
// carrier may name an existing project or propose a new one, and must not
// write a project id. The contract is half of each prompt, so a draft that
// recorded the previous version was produced by a carrier that could not.
var issueDraftPolicyRegistry = map[string]issueDraftPolicy{
	issueDraftPolicyQuestion: {
		Key:       issueDraftPolicyQuestion,
		Version:   "5",
		Guided:    true,
		Behaviour: issueDraftQuestionPolicy,
	},
	issueDraftPolicyConversation: {
		Key:       issueDraftPolicyConversation,
		Version:   "4",
		Guided:    false,
		Behaviour: issueDraftConversationPolicy,
	},
	issueDraftPolicyFrontend: {
		Key:       issueDraftPolicyFrontend,
		Version:   "3",
		Guided:    true,
		Requires:  []string{issueDraftCapabilityGrillFrontendLook},
		Behaviour: issueDraftFrontendPolicy,
	},
}

// issueDraftPolicyRequiredCapabilities resolves a policy's own capability
// requirements into the same shape a client-supplied list resolves to, so the
// two can be merged into one set before anything is assembled or recorded.
func issueDraftPolicyRequiredCapabilities(policy issueDraftPolicy) ([]issueDraftCapability, error) {
	if len(policy.Requires) == 0 {
		return nil, nil
	}
	return resolveIssueDraftCapabilities(policy.Requires)
}

// issueDraftPolicyKeys is the registry's key list, sorted so an error message
// does not depend on map iteration order.
func issueDraftPolicyKeys() []string {
	keys := make([]string, 0, len(issueDraftPolicyRegistry))
	for key := range issueDraftPolicyRegistry {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// issueDraftPolicyByKey resolves a client-supplied policy key. The caller
// decides what an unknown key means; this only answers whether it exists.
func issueDraftPolicyByKey(key string) (issueDraftPolicy, bool) {
	policy, ok := issueDraftPolicyRegistry[key]
	return policy, ok
}

// issueDraftPolicyUnknownMessage is the one wording every rejection of an
// unknown key uses, so the create and switch endpoints cannot drift apart.
func issueDraftPolicyUnknownMessage() string {
	return fmt.Sprintf("policy must be one of: %s", strings.Join(issueDraftPolicyKeys(), ", "))
}

// issueDraftPolicyResponse is the auditable half of a draft's wire shape: which
// policy is running, which version of its prompt the carrier was given, and
// whether that policy asks questions.
//
// Version is read from the draft row, never from the registry — a conversation
// keeps reporting the prompt it actually ran after the registry moves on. A key
// that is no longer registered still reports its recorded version; only
// `guided` falls back, because the client needs a definite answer for how to
// draw the control.
type issueDraftPolicyResponse struct {
	Key     string `json:"key"`
	Version string `json:"version"`
	Guided  bool   `json:"guided"`
}

func issueDraftPolicyResponseFromRow(key, version string) issueDraftPolicyResponse {
	guided := false
	if policy, ok := issueDraftPolicyByKey(key); ok {
		guided = policy.Guided
	}
	return issueDraftPolicyResponse{Key: key, Version: version, Guided: guided}
}
