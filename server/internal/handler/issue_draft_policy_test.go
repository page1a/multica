package handler

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// The alignment policy is the carrier's prompt, named and versioned. These
// tests pin the two properties that make it auditable rather than decorative:
// the draft reports the policy and prompt version it is actually running, and
// switching it rewrites the carrier's instructions — the only thing that
// changes how the next reply behaves — without touching the draft's content or
// its revision.
//
// Database-backed like the rest of the issue-draft suite: "the carrier really
// got this prompt" is a property of the agent row, and a mocked query layer
// would only assert that the handler called what the test told it to.

// carrierInstructions reads the prompt the daemon would hand the carrier on the
// next claim.
func carrierInstructions(t *testing.T, agentID string) string {
	t.Helper()
	var instructions string
	dbfx.QueryRow(t, `SELECT instructions FROM agent WHERE id = $1`, agentID).Scan(&instructions)
	return instructions
}

// defaultInstructions is the prompt a session opened with no capability list
// gets under this policy: the built-in default set, merged with whatever the
// policy itself requires.
//
// It is not `policy.Behaviour`. The installed prompt is the shared contract,
// the capability fragments and the behaviour, and every session these tests
// start is opened without a capability list — so this is the text the carrier's
// agent row actually holds, and comparing against anything else would pass
// while the carrier ran something else.
func defaultInstructions(t *testing.T, policy issueDraftPolicy) string {
	t.Helper()
	capabilities, err := resolveIssueDraftCapabilities(nil)
	if err != nil {
		t.Fatalf("resolving the default capabilities: %v", err)
	}
	required, err := issueDraftPolicyRequiredCapabilities(policy)
	if err != nil {
		t.Fatalf("resolving policy %q's required capabilities: %v", policy.Key, err)
	}
	return policy.Instructions(issueDraftMergeCapabilities(capabilities, required))
}

// carrierPolicyKey reads the policy recorded on the draft row itself — the
// audit trail, as opposed to the API's echo of it.
func carrierPolicyKey(t *testing.T, sessionID string) (string, string) {
	t.Helper()
	var key, version string
	dbfx.QueryRow(t, `
		SELECT policy_key, policy_version FROM issue_draft WHERE chat_session_id = $1
	`, sessionID).Scan(&key, &version)
	return key, version
}

func switchPolicyRequest(t *testing.T, sessionID, policy string) *http.Request {
	t.Helper()
	return withURLParam(newRequest(http.MethodPatch, "/api/issue-drafts/"+sessionID+"/policy", map[string]any{
		"policy": policy,
	}), "sessionId", sessionID)
}

// A create with no policy gets the guided default, and the prompt that goes
// with it. The question block is asserted by name because it is the contract
// `packages/core/issue-drafts/protocol.ts` parses for the answer chips: a
// version bump that dropped it would silently disable them.
func TestIssueDraftDefaultsToGuidedQuestionPolicy(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)

	questionPolicy, ok := issueDraftPolicyByKey(issueDraftPolicyQuestion)
	if !ok {
		t.Fatal("the guided question policy is not registered")
	}
	if session.Draft.Policy.Key != issueDraftPolicyQuestion {
		t.Fatalf("policy key = %q, want %q", session.Draft.Policy.Key, issueDraftPolicyQuestion)
	}
	if session.Draft.Policy.Version != questionPolicy.Version {
		t.Fatalf("policy version = %q, want the registered %q", session.Draft.Policy.Version, questionPolicy.Version)
	}
	if !session.Draft.Policy.Guided {
		t.Fatal("the guided policy reports guided = false")
	}

	recordedKey, recordedVersion := carrierPolicyKey(t, session.SessionID)
	if recordedKey != questionPolicy.Key || recordedVersion != questionPolicy.Version {
		t.Fatalf("draft row recorded %s@%s, want %s@%s", recordedKey, recordedVersion, questionPolicy.Key, questionPolicy.Version)
	}

	instructions := carrierInstructions(t, session.AgentID)
	if instructions != defaultInstructions(t, questionPolicy) {
		t.Fatal("carrier was not given the registered question prompt")
	}
	if !strings.Contains(instructions, "<issue_draft_question>") {
		t.Fatal("the guided prompt does not describe the question block the client parses")
	}
	if !strings.Contains(instructions, "<issue_draft>") {
		t.Fatal("the guided prompt lost the draft block contract")
	}
}

// The whole point of recording a version: it can be read back, and it names a
// prompt that is in the registry — an audit that pointed at nothing would be
// worse than no audit.
func TestIssueDraftPolicyVersionIsAuditable(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)

	var out ListIssueDraftsResponse
	testutil.Call(t, testHandler.ListIssueDrafts, newRequest(http.MethodGet, "/api/issue-drafts", nil)).
		Want(http.StatusOK).JSON(&out)

	var listed *IssueDraftSummary
	for i := range out.Drafts {
		if out.Drafts[i].ChatSessionID == session.SessionID {
			listed = &out.Drafts[i]
			break
		}
	}
	if listed == nil {
		t.Fatal("the draft that was just created is missing from the unfinished list")
	}
	policy, ok := issueDraftPolicyByKey(listed.Policy.Key)
	if !ok {
		t.Fatalf("listed draft reports policy %q, which is not registered", listed.Policy.Key)
	}
	if listed.Policy.Version != policy.Version {
		t.Fatalf("listed draft reports %s@%s, want %s@%s",
			listed.Policy.Key, listed.Policy.Version, policy.Key, policy.Version)
	}
}

// Switching policy is two writes that must not drift: the row records the
// version, and the carrier gets the prompt. It must also leave the draft itself
// — content and revision — exactly as it was, or a policy change would reject
// the user's next save with a conflict they cannot explain.
func TestSwitchIssueDraftPolicyRewritesCarrierPromptOnly(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	saved := saveIssueDraft(t, session.SessionID, 0, "ready", map[string]any{
		"title":       "Keep my draft",
		"description": "Policy changes must not rewrite this.",
	})

	var switched issueDraftResponse
	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, issueDraftPolicyConversation)).
		Want(http.StatusOK).JSON(&switched)

	conversationPolicy, ok := issueDraftPolicyByKey(issueDraftPolicyConversation)
	if !ok {
		t.Fatal("the conversation policy is not registered")
	}
	if switched.Policy.Key != issueDraftPolicyConversation || switched.Policy.Version != conversationPolicy.Version {
		t.Fatalf("switch reported %s@%s, want %s@%s",
			switched.Policy.Key, switched.Policy.Version, conversationPolicy.Key, conversationPolicy.Version)
	}
	if switched.Policy.Guided {
		t.Fatal("the unguided policy reports guided = true")
	}
	if switched.Revision != saved.Revision {
		t.Fatalf("policy switch moved the revision from %d to %d", saved.Revision, switched.Revision)
	}
	if string(switched.Draft) != string(saved.Draft) {
		t.Fatalf("policy switch rewrote the draft: %s -> %s", saved.Draft, switched.Draft)
	}
	if switched.Status != "ready" {
		t.Fatalf("policy switch moved status to %q, want ready", switched.Status)
	}

	recordedKey, recordedVersion := carrierPolicyKey(t, session.SessionID)
	if recordedKey != conversationPolicy.Key || recordedVersion != conversationPolicy.Version {
		t.Fatalf("draft row recorded %s@%s after the switch, want %s@%s",
			recordedKey, recordedVersion, conversationPolicy.Key, conversationPolicy.Version)
	}
	if got := carrierInstructions(t, session.AgentID); got != defaultInstructions(t, conversationPolicy) {
		t.Fatal("carrier kept the old prompt after the policy switch")
	}

	// And back, so the endpoint is a real toggle rather than a one-way door.
	questionPolicy, _ := issueDraftPolicyByKey(issueDraftPolicyQuestion)
	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, issueDraftPolicyQuestion)).
		Want(http.StatusOK)
	if got := carrierInstructions(t, session.AgentID); got != defaultInstructions(t, questionPolicy) {
		t.Fatal("carrier did not get the question prompt back after switching back")
	}
}

// An unknown key is refused, not defaulted: a session that looks switched while
// running the old prompt is exactly the drift the recorded version exists to
// prevent. The refusal also has to name the keys, because it is the only place a
// client author is told what they are — the front-end entry is the one most
// likely to be guessed as "grill" rather than by its key.
func TestIssueDraftPolicyRejectsUnknownKey(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	const wantMessage = "policy must be one of: conversation, frontend, question"

	// On create.
	refused := testutil.Call(t, testHandler.CreateIssueDraftSession, newRequest(http.MethodPost, "/api/issue-drafts", map[string]any{
		"runtime_id": testRuntimeID,
		"policy":     "interrogation",
	})).Want(http.StatusBadRequest).Map()
	if refused["error"] != wantMessage {
		t.Fatalf("create refused an unknown policy with %v, want %q", refused["error"], wantMessage)
	}

	// And on switch, leaving the carrier's prompt alone.
	session := startIssueDraftSession(t)
	questionPolicy, _ := issueDraftPolicyByKey(issueDraftPolicyQuestion)
	before := carrierInstructions(t, session.AgentID)

	switched := testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, "interrogation")).
		Want(http.StatusBadRequest).Map()
	if switched["error"] != wantMessage {
		t.Fatalf("switch refused an unknown policy with %v, want %q", switched["error"], wantMessage)
	}

	if got := carrierInstructions(t, session.AgentID); got != before {
		t.Fatal("a refused policy switch still rewrote the carrier prompt")
	}
	if key, _ := carrierPolicyKey(t, session.SessionID); key != questionPolicy.Key {
		t.Fatalf("a refused policy switch recorded policy %q", key)
	}
}

// Every key the registry lists has to survive the endpoints, not just the
// registry: create and switch both resolve through the same lookup, and a key
// the picker offers but an endpoint refuses is a switch that cannot land. The
// version recorded on the row is the audit half, so it is asserted here for
// every entry rather than only for the default.
func TestIssueDraftPolicyKeysAreSwitchable(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	for _, key := range issueDraftPolicyKeys() {
		policy, ok := issueDraftPolicyByKey(key)
		if !ok {
			t.Fatalf("issueDraftPolicyKeys listed %q, which the registry does not hold", key)
		}

		session := startIssueDraftSession(t)
		var switched issueDraftResponse
		testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, key)).
			Want(http.StatusOK).JSON(&switched)

		if switched.Policy.Key != key || switched.Policy.Version != policy.Version || switched.Policy.Guided != policy.Guided {
			t.Fatalf("switching to %q reported %+v, want %s@%s guided=%t",
				key, switched.Policy, policy.Key, policy.Version, policy.Guided)
		}
		if recorded, version := carrierPolicyKey(t, session.SessionID); recorded != key || version != policy.Version {
			t.Fatalf("switching to %q recorded %s@%s on the draft row, want %s@%s",
				key, recorded, version, policy.Key, policy.Version)
		}
		if got := carrierInstructions(t, session.AgentID); got != defaultInstructions(t, policy) {
			t.Fatalf("switching to %q did not install that entry's prompt", key)
		}
	}
}

// Switching is a write on someone else's conversation if ownership is not
// checked, and the only thing it changes is what their carrier will be told.
func TestSwitchIssueDraftPolicyRequiresOwnership(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	otherUser := dbfx.User(t, "Issue Draft Policy Outsider", "issue-draft-policy-outsider@multica.ai")
	dbfx.Member(t, testWorkspaceID, otherUser, "member")

	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, testutil.WithHeaders(
		switchPolicyRequest(t, session.SessionID, issueDraftPolicyConversation),
		"X-User-ID", otherUser,
	)).Want(http.StatusNotFound)

	questionPolicy, _ := issueDraftPolicyByKey(issueDraftPolicyQuestion)
	if got := carrierInstructions(t, session.AgentID); got != defaultInstructions(t, questionPolicy) {
		t.Fatal("a rejected policy switch rewrote the carrier prompt")
	}
}

// A reply already in flight was claimed with the previous prompt, so switching
// under it would make the switch look like it did not take. Same gate as the
// runtime switch.
func TestSwitchIssueDraftPolicyRefusesWhileTurnIsPending(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	dbfx.Task(t, session.AgentID, testutil.Cols{
		"chat_session_id": session.SessionID,
		"runtime_id":      testRuntimeID,
		"status":          "queued",
	})

	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, issueDraftPolicyConversation)).
		Want(http.StatusConflict)

	questionPolicy, _ := issueDraftPolicyByKey(issueDraftPolicyQuestion)
	if got := carrierInstructions(t, session.AgentID); got != defaultInstructions(t, questionPolicy) {
		t.Fatal("a refused policy switch rewrote the carrier prompt")
	}
}

// The registry is configuration, and configuration rots quietly: every entry
// must carry a prompt and a version, keys must match their entry, and the
// guided policy must be the one that explains the question block.
func TestIssueDraftPolicyRegistryIsWellFormed(t *testing.T) {
	if len(issueDraftPolicyRegistry) == 0 {
		t.Fatal("no alignment policies are registered")
	}
	for key, policy := range issueDraftPolicyRegistry {
		if policy.Key != key {
			t.Fatalf("registry entry %q carries key %q", key, policy.Key)
		}
		if strings.TrimSpace(policy.Version) == "" {
			t.Fatalf("policy %q has no version", key)
		}
		if strings.TrimSpace(policy.Behaviour) == "" {
			t.Fatalf("policy %q has no prompt", key)
		}
		if !strings.Contains(defaultInstructions(t, policy), "<issue_draft>") {
			t.Fatalf("policy %q does not carry the shared draft-block contract", key)
		}
	}
	guided, ok := issueDraftPolicyByKey(issueDraftPolicyQuestion)
	if !ok || !guided.Guided {
		t.Fatal("the guided default is not registered as guided")
	}
	if !strings.Contains(defaultInstructions(t, guided), "<issue_draft_question>") {
		t.Fatal("the guided policy does not describe the question block")
	}
	plain, ok := issueDraftPolicyByKey(issueDraftPolicyConversation)
	if !ok || plain.Guided {
		t.Fatal("the conversation policy is missing or reports itself as guided")
	}
	if strings.Contains(defaultInstructions(t, plain), "<issue_draft_question>") {
		t.Fatal("the unguided policy still teaches the question block")
	}

	// `guided` is what the client renders answer chips from, so a guided entry
	// that never describes the block asks questions nobody can click, and an
	// unguided one that does interviews after the user asked for conversation.
	// The registry grows, so this is a loop rather than three named checks.
	for key, policy := range issueDraftPolicyRegistry {
		teaches := strings.Contains(defaultInstructions(t, policy), "<issue_draft_question>")
		if policy.Guided && !teaches {
			t.Fatalf("policy %q reports itself guided but never describes the question block", key)
		}
		if !policy.Guided && teaches {
			t.Fatalf("policy %q reports itself unguided but still teaches the question block", key)
		}
	}
}

// The front-end section is the shared contract's, not a policy's, so it reaches
// the carrier whichever policy is running: a draft that skipped the screen must
// not be indistinguishable from one whose request genuinely has no surface.
// Version 3 is the record of the contract that started asking about the screen
// — a draft that recorded "2" was produced by one that never did.
func TestIssueDraftContractAsksForTheFrontendSection(t *testing.T) {
	for _, want := range []string{
		// When a request counts as having a surface, and the tie-break:
		// unsure means "has one", because the two mistakes are not symmetric.
		"A request has a surface when any outcome in it changes what a person sees or does on a screen",
		"When you cannot tell, assume it has one",
		// The fixed heading, in the language the description is written in.
		`"## 前端做法"`,
		`"## Frontend"`,
		// The five lines one screen group is made of.
		"- Name:",
		"- Entry:",
		"- Main action:",
		"- States:",
		"- Reuse:",
		// A child's spec comes back with its key, or the group rewrite
		// silently deletes it.
		"carry it back unchanged in every later block",
	} {
		if !strings.Contains(issueDraftContract, want) {
			t.Fatalf("the shared contract does not carry %q", want)
		}
	}

	for key, policy := range issueDraftPolicyRegistry {
		if !strings.Contains(defaultInstructions(t, policy), "## 前端做法") {
			t.Fatalf("policy %q does not carry the front-end section", key)
		}
	}

	// Version 3 is the contract half's record and both entries share it, so the
	// unguided entry — whose own prompt never changed — stops there. The guided
	// entry moved on to 4 when its behaviour grew the two calls that are the
	// user's to make, asserted separately below.
	plain, ok := issueDraftPolicyByKey(issueDraftPolicyConversation)
	if !ok {
		t.Fatal("the conversation policy is not registered")
	}
	if plain.Version != "4" {
		t.Fatalf("the conversation policy is at version %q, want 4", plain.Version)
	}
}

// The two calls the carrier must not make for the user: which screens are in
// this issue, and which direction the surface takes. The guided policy asks
// both — the second one exactly once, because it cannot show a picture. The
// unguided policy never interviews, so the same rules must not reach it. Version
// 4 is the record of the guided prompt that started asking; the conversation
// entry stays at 3 because its own prompt did not change.
func TestIssueDraftQuestionPolicyHandsScopeAndDirectionToTheUser(t *testing.T) {
	guided, ok := issueDraftPolicyByKey(issueDraftPolicyQuestion)
	if !ok || !guided.Guided {
		t.Fatal("the guided question policy is not registered as guided")
	}
	if guided.Version != "5" {
		t.Fatalf("the guided policy is at version %q, want 5", guided.Version)
	}
	for _, want := range []string{
		// Scope: the priority call is the user's, and the carrier proposes.
		"two things are the user's to decide and yours only to propose",
		"Which screens are in THIS issue and which wait for later",
		"Ask it with the split you would choose marked recommended",
		// Direction: asked once, with named options, then stopped.
		"The direction the surface takes",
		"Ask it once, with 2-4 named directions — then stop",
		"a second question about the look buys nothing",
		// Timing: the surface is settled before the rest of the draft.
		"Ask the surface question before the rest of the draft is settled",
	} {
		if !strings.Contains(defaultInstructions(t, guided), want) {
			t.Fatalf("the guided prompt does not carry %q", want)
		}
	}

	plain, ok := issueDraftPolicyByKey(issueDraftPolicyConversation)
	if !ok || plain.Guided {
		t.Fatal("the conversation policy is missing or reports itself as guided")
	}
	if strings.Contains(defaultInstructions(t, plain), "two things are the user's to decide and yours only to propose") {
		t.Fatal("the unguided policy still hands the surface to the user to decide")
	}
}

// The front-end entry is the only policy that runs the look round. The two
// text-only entries keep the contract's rule that a look is not settled in
// prose; this one earns the exception the only way that rule allows — by
// producing a file the user can open. Both halves are pinned here, because the
// failure modes are opposite: an entry that lost the upload step is a policy
// that only talks about prototypes, and a text-only entry that grew one is the
// requirement interview suddenly spending its turns drawing.
//
// Version 1 is the record of a new entry, not a changed one: there is no
// earlier prompt of it for a draft to have run.
func TestIssueDraftFrontendPolicyRunsTheLookRound(t *testing.T) {
	frontend, ok := issueDraftPolicyByKey(issueDraftPolicyFrontend)
	if !ok {
		t.Fatal("the front-end policy is not registered")
	}
	if !frontend.Guided {
		t.Fatal("the front-end policy reports itself unguided, so its one question at a time would never render answer chips")
	}
	if frontend.Version != "3" {
		t.Fatalf("the front-end policy is at version %q, want 3", frontend.Version)
	}
	for _, want := range []string{
		// The user picked this style, so the round starts instead of being
		// offered again — the offer belongs to the policies that do not have it.
		"run the look round — do not offer it again",
		// The default unit, and the comparison mode with its own tie-break. Both
		// live in the `grill-frontend-look` capability now, and they reach this
		// prompt because the policy declares it as a requirement.
		"One screen at a time is the default",
		"Five structural directions",
		"one file behind a picker",
		// The artifact, and the judgement that makes the style worth having.
		"multica attachment upload",
		"A round is not finished until the user has something to open",
		// Widths and states: what makes it openable rather than a mock.
		"desktop AND phone width",
		"loading / empty / error",
		// The round stops at two screens; past that it becomes an implementation
		// issue rather than a longer alignment.
		"At most two screens in one alignment",
		// The settled prototype has to survive into the draft, or whoever opens
		// the issue cannot see what was agreed.
		`"Prototype:"`,
		`"原型："`,
	} {
		if !strings.Contains(defaultInstructions(t, frontend), want) {
			t.Fatalf("the front-end prompt does not carry %q", want)
		}
	}

	// The look round reaches a text-only policy only when the user turns the
	// capability on, and the *policy's own half* must never carry it: a
	// requirement interview that grew a prototyping step on its own is the
	// failure this boundary exists to catch. `frontend` is the opposite case —
	// it declares the capability, so the round is part of what it means.
	for _, key := range []string{issueDraftPolicyQuestion, issueDraftPolicyConversation} {
		policy, ok := issueDraftPolicyByKey(key)
		if !ok {
			t.Fatalf("policy %q is not registered", key)
		}
		if strings.Contains(policy.Behaviour, "multica attachment upload") {
			t.Fatalf("policy %q builds prototypes on its own; the look round belongs to %q, or to a capability the user turns on", key, issueDraftPolicyFrontend)
		}
	}
	if !slices.Contains(frontend.Requires, issueDraftCapabilityGrillFrontendLook) {
		t.Fatalf("the front-end policy does not require the %q capability, so the round it names has no method", issueDraftCapabilityGrillFrontendLook)
	}
}

// Finalize is the structured direct-write path this whole flow exists to
// protect: a policy switch must not become a second way to create an issue, and
// it must not be required before confirming.
func TestPolicySwitchDoesNotCreateAnIssue(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cleanupIssueDraftCarriers(t)

	session := startIssueDraftSession(t)
	testutil.Call(t, testHandler.SwitchIssueDraftPolicy, switchPolicyRequest(t, session.SessionID, issueDraftPolicyConversation)).
		Want(http.StatusOK)

	if got := dbfx.Count(t, `
		SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND origin_type = 'issue_draft'
	`, testWorkspaceID); got != 0 {
		t.Fatalf("switching policy created %d issues", got)
	}
}
