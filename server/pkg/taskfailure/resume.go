package taskfailure

import (
	"regexp"
	"strings"
)

// UnresumableHistory reports whether an agent error means the conversation
// history itself can no longer be sent to the provider: a message already
// baked into the transcript carries empty content, so every resume of that
// session replays the same body and reproduces the same rejection.
//
// This predicate is deliberately provider-agnostic, and that is the whole
// point. The original detector paired "400" with "invalid_request_error"
// (see classifyPoisonedError in internal/daemon), which is the Anthropic wire
// shape. The identical defect is reported by other providers with neither
// token present:
//
//	Invalid request: the message at position 37 with role 'assistant' must not be empty   (GH #6066)
//	provider.api_error: 400 the message at position 43 with role 'assistant' must not be empty  (GH #5760)
//	messages.37: all messages must have non-empty content ...                             (Anthropic)
//	messages[43].content: content must not be empty
//
// Keying on a status code or a provider name means missing the next backend
// that words it differently — Multica supports 17 of them and holds only an
// opaque session id, so it cannot know which CLIs write a truncated
// transcript. What all of these DO state is the two things that define the
// defect: that some content is empty, and which message in the history it
// belongs to.
//
// Both signals are required, and that is what keeps the predicate narrow. A
// tool reporting "commit message must not be empty" has no message locator
// and does not match; a diff mentioning "messages[3]" without an emptiness
// complaint does not match either. Erring toward NOT matching is the safe
// direction: a miss leaves today's behaviour (the task fails and the user
// retries), while a false positive would discard a healthy session pointer
// and lose conversation context.
func UnresumableHistory(errText string) bool {
	if errText == "" {
		return false
	}
	return emptyContentRe.MatchString(errText) && historyMessageLocatorRe.MatchString(errText)
}

// AuthMethodUnresolved reports whether an agent error is the provider
// SDK refusing to resolve its own credentials — no api_key, no auth_token, and
// no explicitly-omitted auth header. On a RESUMED session this is
// deterministic rather than transient: the credential-bearing provider
// identity lives in the session state the runtime rebuilt, so replaying the
// same session reproduces the same error forever. A fresh session re-resolves
// the provider from current config and succeeds (GH #6777).
//
// Deliberately the exact provider phrase and nothing looser. Every other
// authentication-shaped error — an expired token, a revoked key, a 401 — is
// about the credential itself and is NOT cured by a new session, so widening
// this would discard healthy conversation pointers on failures a retry cannot
// fix. Classify leaves this text as agent_error.unknown (resume-safe), which
// is why the guard has to key on the text rather than the reason.
//
// This is the single source of truth for the phrase. Keep it in sync with the
// GetLastTaskSession / GetLastChatTaskSession resume queries (pkg/db/queries),
// which apply the same guard server-side so rows written by a daemon too old
// to carry the in-turn retry are still excluded from resume.
func AuthMethodUnresolved(errText string) bool {
	if errText == "" {
		return false
	}
	return strings.Contains(strings.ToLower(errText), authMethodUnresolvedPhrase)
}

// authMethodUnresolvedPhrase is the lowercase provider phrase
// AuthMethodUnresolved matches. It appears verbatim in the runtime's error
// however the failure reached us — session/resume, session/set_model or
// session/prompt — because every ACP adapter wraps the underlying message
// with %v rather than replacing it.
const authMethodUnresolvedPhrase = "could not resolve authentication method"

// AntigravitySessionTokenExpired reports whether an agent error is Google
// rejecting the Antigravity CLI (agy) because the OAuth access token its
// RUNNING process holds is no longer valid (DENE-724).
//
// The defect this names is not a credential the user has to renew. `agy -p`
// reads its access token ONCE at process start and never refreshes it
// mid-turn, so a run that outlives that token's remaining lifetime dies with
// Google's UNAUTHENTICATED 401 no matter how much work it had already done —
// while every short invocation re-reads the login store and succeeds. That is
// why "the CLI works fine on my machine" and a failing Multica run are the
// same account, and why telling the member to sign in again sends them
// nowhere.
//
// What the failure DOES prove is that this session is finished: the turn's
// transcript ends in a rejected call, and the remedy is a NEW agy process
// (which reloads the login) rather than another resume of the same
// conversation. The daemon classifies it as
// antigravity_session_token_expired and retires the session; the resume
// queries in pkg/db/queries apply the same phrase guard, which is the only
// protection for rows an older daemon wrote under
// agent_error.provider_auth_or_access. Keep the three in sync.
//
// This is the Google-side wording only. The CLI's own logged-out notice is a
// different sentence and a different reason — see AntigravityNotLoggedIn for
// why the two must not share copy.
func AntigravitySessionTokenExpired(errText string) bool {
	return containsAnyPhrase(errText, antigravitySessionTokenExpiredPhrases)
}

// AntigravityNotLoggedIn reports whether the Antigravity CLI (agy) itself said
// it has no usable login on the machine, rather than Google rejecting a token
// mid-run (DENE-724).
//
// Why this is separate from AntigravitySessionTokenExpired even though the fix
// for both is the same session retirement: they call for different words. The
// 401 above proves the saved login is fine (a short invocation reloads and
// succeeds), so its copy must NOT send the member to sign in. This notice is
// the CLI saying it could not use a login at all — the one symptom that really
// can mean the account is signed out — so its copy must not claim "your login
// is fine" either. Collapsing the two into one reason forces one sentence to
// be wrong for the other audience, which is exactly the DENE-724 review
// finding.
//
// The sentence is ambiguous on its own: agy prints it both when the stored
// login is genuinely gone and when the credential store it consulted could not
// authenticate the process, so the copy leads with a check the member can run
// (`agy -p ping`) and branches on the result.
func AntigravityNotLoggedIn(errText string) bool {
	return containsAnyPhrase(errText, antigravityNotLoggedInPhrases)
}

// AntigravityResumeUnsafe reports whether an agent error carries either
// Antigravity wording that makes the recorded session unusable, whatever the
// reason recorded against it.
//
// This is the phrase guard, not a classifier: the resume queries in
// pkg/db/queries cannot see which backend produced a row, and the rows
// DENE-724 was filed from were written by a daemon that only knew
// agent_error.provider_auth_or_access — a reason every resume filter treats as
// safe. Matching the raw text is the only thing that keeps those exact rows
// from being resumed a third time. service.ResumeUnsafeFailure reads the same
// predicate so the manual-retry path cannot disagree with the SQL.
func AntigravityResumeUnsafe(errText string) bool {
	return AntigravitySessionTokenExpired(errText) || AntigravityNotLoggedIn(errText)
}

func containsAnyPhrase(errText string, phrases []string) bool {
	if errText == "" {
		return false
	}
	lowered := strings.ToLower(errText)
	for _, phrase := range phrases {
		if strings.Contains(lowered, phrase) {
			return true
		}
	}
	return false
}

// antigravitySessionTokenExpiredPhrases is Google's own wording when it
// rejects an access token that has expired or been invalidated:
//
//	UNAUTHENTICATED (code 401): Request had invalid authentication credentials.
//	  Expected OAuth 2 access token, login cookie or other valid authentication
//	  credential. ...
//
// Matching is phrase-based rather than provider-based on purpose: the SQL
// guard cannot see which backend produced the row. The phrase is Google's, so
// no other provider can trip it, and a bare "401" is deliberately not a marker
// — every provider emits one, and on most of them it really does mean a
// credential the user must renew.
var antigravitySessionTokenExpiredPhrases = []string{
	"request had invalid authentication credentials",
}

// antigravityNotLoggedInPhrases is the Antigravity CLI's own logged-out
// notice, which carries no status code at all — another reason the predicates
// cannot key on "401":
//
//	You are not logged into Antigravity
var antigravityNotLoggedInPhrases = []string{
	"not logged into antigravity",
}

// emptyContentRe matches the provider's complaint that a content field is
// empty, in the wordings observed across providers.
var emptyContentRe = regexp.MustCompile(`(?i)must not be empty|must be non-?empty|must have non-?empty|non-?empty content|cannot be empty|should not be empty`)

// historyMessageLocatorRe matches the part of the error that points at a
// message inside the conversation history — a role name, an index, or a
// position. Without one of these an emptiness complaint is about some other
// field entirely and says nothing about the transcript.
//
// Keep in sync with the equivalent regex in the GetLastTaskSession /
// GetLastChatTaskSession resume queries (pkg/db/queries), which apply the same
// text guard server-side for rows an older daemon classified as
// agent_error.unknown.
var historyMessageLocatorRe = regexp.MustCompile(`(?i)role[^a-z0-9]{0,2}assistant|assistant message|message at position|messages\.[0-9]|messages\[[0-9]`)
