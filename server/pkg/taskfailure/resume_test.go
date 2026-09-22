package taskfailure

import "testing"

// TestUnresumableHistory pins the predicate against the real provider wordings
// collected from the field. The positives are verbatim strings from user
// reports; the negatives are the shapes that must keep resuming, because a
// false positive throws away a healthy conversation.
func TestUnresumableHistory(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		errMsg string
		want   bool
	}{
		// --- Positives: an empty message is baked into the transcript. ---
		{
			// GH #6066, verbatim from the reporter's screenshot. Carries
			// neither "invalid_request_error" nor a bare "400", which is
			// exactly why the Anthropic-shaped detector missed it.
			name:   "gh6066 invalid request with position and role",
			errMsg: "Invalid request: the message at position 37 with role 'assistant' must not be empty",
			want:   true,
		},
		{
			// GH #5760, from a manual `kimi -S <session> -p ping` repro.
			name:   "gh5760 kimi provider api_error",
			errMsg: "provider.api_error: 400 the message at position 43 with role 'assistant' must not be empty",
			want:   true,
		},
		{
			name:   "kimi wrapped by the acp sniffer",
			errMsg: "kimi provider error: the message at position 43 with role 'assistant' must not be empty",
			want:   true,
		},
		{
			// Anthropic's own wording for the same defect.
			name:   "anthropic indexed message non-empty content",
			errMsg: `API Error: 400 {"type":"error","error":{"type":"invalid_request_error","message":"messages.37: all messages must have non-empty content except for the optional final assistant message"}}`,
			want:   true,
		},
		{
			name:   "openai-compatible indexed content field",
			errMsg: "messages[43].content: content must not be empty",
			want:   true,
		},
		{
			name:   "double-quoted role",
			errMsg: `Invalid request: the message with role "assistant" must not be empty`,
			want:   true,
		},
		{
			name:   "unspaced role token",
			errMsg: "invalid_request: role=assistant content cannot be empty",
			want:   true,
		},
		{
			name:   "prose wording",
			errMsg: "The final assistant message must not be empty.",
			want:   true,
		},

		// --- Negatives: emptiness complaints that are NOT about history. ---
		{
			// The single most likely false positive: a tool or validator
			// complaining about a field. No locator into the message history.
			name:   "tool validation error",
			errMsg: "validation error: field must not be empty",
			want:   false,
		},
		{
			name:   "git commit message",
			errMsg: "Aborting commit due to empty commit message: commit message must not be empty",
			want:   false,
		},
		{
			name:   "empty api key",
			errMsg: "config error: api key must not be empty",
			want:   false,
		},

		// --- Negatives: history locator without an emptiness complaint. ---
		{
			name:   "oversized image in history",
			errMsg: "messages[3].content[0].image.source.base64.data: image dimensions exceed max allowed size",
			want:   false,
		},
		{
			name:   "assistant message mentioned in a normal error",
			errMsg: "could not render assistant message: template failure",
			want:   false,
		},

		// --- Negatives: transient failures that MUST keep the session. ---
		{
			name:   "rate limit",
			errMsg: "API Error: 429 rate_limit_error: too many requests",
			want:   false,
		},
		{
			name:   "provider 5xx",
			errMsg: "provider.api_error: 503 upstream temporarily unavailable",
			want:   false,
		},
		{
			name:   "network drop",
			errMsg: "Connection closed mid-response",
			want:   false,
		},
		{
			name:   "empty input",
			errMsg: "",
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := UnresumableHistory(tc.errMsg); got != tc.want {
				t.Fatalf("UnresumableHistory(%q) = %v, want %v", tc.errMsg, got, tc.want)
			}
		})
	}
}

// TestUnresumableHistoryIsStatusAndProviderAgnostic is the regression that
// keeps this from sliding back into a per-provider string match. The same
// defect wearing four different provider costumes must classify identically —
// that is the property GH #6066 and GH #5760 both needed and neither had.
func TestUnresumableHistoryIsStatusAndProviderAgnostic(t *testing.T) {
	t.Parallel()

	sameDefect := []string{
		"Invalid request: the message at position 37 with role 'assistant' must not be empty",
		"provider.api_error: 400 the message at position 43 with role 'assistant' must not be empty",
		"kimi provider error: the message at position 1 with role 'assistant' must not be empty",
		"messages.12: all messages must have non-empty content",
	}
	for _, errMsg := range sameDefect {
		if !UnresumableHistory(errMsg) {
			t.Errorf("provider wording must not change the verdict, missed: %q", errMsg)
		}
	}
}

// TestAuthMethodUnresolved pins the GH #6777 predicate. The positives are the
// provider phrase as it reaches us through each ACP lifecycle step; the
// negatives are every other authentication-shaped failure, which a fresh
// session cannot cure and which must therefore keep its session pointer.
func TestAuthMethodUnresolved(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		errMsg string
		want   bool
	}{
		{
			// Verbatim from the reporter, as the ACP provider-error sniffer
			// wraps it (GH #6777).
			name:   "gh6777 sniffer-wrapped provider error",
			errMsg: `hermes provider error: "Could not resolve authentication method. Expected either api_key or auth_token to be set. Or for one of the X-Api-Key or Authorization headers to be explicitly omitted"`,
			want:   true,
		},
		{
			// Same failure one step earlier: the adapter wraps the runtime
			// message with %v instead of replacing it, so the phrase survives.
			name:   "set_model wrapper keeps the phrase",
			errMsg: `hermes could not switch to model "custom:deepseek-v4-pro": session/set_model: Could not resolve authentication method. Expected either api_key or auth_token to be set.`,
			want:   true,
		},
		{
			name:   "case differences do not matter",
			errMsg: "COULD NOT RESOLVE AUTHENTICATION METHOD",
			want:   true,
		},
		{
			name:   "empty error",
			errMsg: "",
			want:   false,
		},

		// --- Negatives: authentication-shaped, but not session-shaped. A new
		// session replays these verbatim, so matching them would cost a wasted
		// run AND the conversation pointer. ---
		{
			name:   "rejected api key",
			errMsg: `hermes provider error: "401 authentication_error: invalid x-api-key"`,
			want:   false,
		},
		{
			name:   "revoked oauth token",
			errMsg: "OAuth access token has been revoked",
			want:   false,
		},
		{
			name:   "missing credential env var",
			errMsg: "missing environment variable: ANTHROPIC_API_KEY",
			want:   false,
		},
		{
			// Talks about resolving and about auth, but is a DNS failure.
			name:   "unresolved host is not unresolved auth",
			errMsg: "dial tcp: lookup auth.example.com: no such host",
			want:   false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := AuthMethodUnresolved(tt.errMsg); got != tt.want {
				t.Errorf("AuthMethodUnresolved(%q) = %v, want %v", tt.errMsg, got, tt.want)
			}
		})
	}
}

// TestAuthMethodUnresolvedMatchesResumeQueryGuard asserts the Go predicate and
// the SQL guard in GetLastTaskSession / GetLastChatTaskSession agree on the
// phrase. They are two independent implementations of the same rule — the
// daemon's in-turn retry reads this one, and rows written by a daemon too old
// to have it are caught by the SQL one — so a drift would leave one layer
// resuming a session the other considers dead.
func TestAuthMethodUnresolvedMatchesResumeQueryGuard(t *testing.T) {
	t.Parallel()

	// The ILIKE pattern the queries apply, minus the wildcards.
	const sqlGuardPhrase = "could not resolve authentication method"

	if !AuthMethodUnresolved("prefix " + sqlGuardPhrase + " suffix") {
		t.Fatalf("predicate does not match the SQL guard phrase %q — pkg/db/queries and pkg/taskfailure have drifted", sqlGuardPhrase)
	}
}

// dene724AntigravityError is DENE-724's failure verbatim, as the daemon logged
// it for task 01a0c372-0d6d-7d56-9c8f-9c725671e987 after a 29m37s Antigravity
// run — and again for the follow-up 01a0c3a3-d346-7105-8126-47e33a56f862,
// which resumed the same conversation and spent another 35m58s to reach the
// identical 401. agy loads its OAuth access token once at process start and
// never refreshes it, so the run dies when that token's lifetime runs out,
// however much work it had already done.
const dene724AntigravityError = `UNAUTHENTICATED (code 401): Request had invalid authentication credentials. ` +
	`Expected OAuth 2 access token, login cookie or other valid authentication credential. ` +
	`See https://developers.google.com/identity/sign-in/web/devconsole-project.; ` +
	`agy stderr: error: UNAUTHENTICATED (code 401): Request had invalid authentication credentials. ` +
	`Expected OAuth 2 access token, login cookie or other valid authentication credential. ` +
	`See https://developers.google.com/identity/sign-in/web/devconsole-project.` + "\n" +
	`AGY_ERROR: {"short_error":"UNAUTHENTICATED (code 401): Request had invalid authentication credentials.",` +
	`"status":"UNAUTHENTICATED","error_code":401,"code_kind":"http","retryable":false,` +
	`"error_id":"a869a7c3-252f-4028-a01c-fcceb364f369-102"}`

// dene724NotLoggedInError is the Antigravity CLI's own logged-out notice, the
// second wording DENE-724 lists. It is a different sentence from the 401 above
// and gets a different reason: it can mean the account really is signed out, so
// its copy must not tell the member their login is fine. It carries no status
// code at all, which is why the predicates cannot key on "401".
const dene724NotLoggedInError = "error: You are not logged into Antigravity"

// TestAntigravitySessionTokenExpired pins the Google-side predicate against the
// real Antigravity 401 wording. The positives decide whether a dead session is
// retired; the negatives decide whether a credential the member genuinely has to
// renew keeps its conversation. Both directions matter, and the negatives are
// the dangerous side: a false positive silently drops conversation context.
//
// The CLI's own logged-out notice is deliberately NOT a positive here — it has
// its own predicate and reason (see TestAntigravityNotLoggedIn), because one
// sentence cannot be right for both audiences.
func TestAntigravitySessionTokenExpired(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		errMsg string
		want   bool
	}{
		{
			name:   "dene724 daemon error verbatim",
			errMsg: dene724AntigravityError,
			want:   true,
		},
		{
			name:   "wrapped by the daemon's stderr tail",
			errMsg: "agy provider error: Request had invalid authentication credentials.",
			want:   true,
		},
		{
			// Ownership split: the CLI's notice belongs to the other
			// predicate. Matching both here is what let one reason's copy
			// claim "your login is still fine" to a member who is signed out.
			name:   "agy not-logged-in notice is not this predicate",
			errMsg: dene724NotLoggedInError,
			want:   false,
		},

		// --- Negatives: auth failures a new session cannot cure. ---
		{
			name:   "anthropic bare 401",
			errMsg: "API Error: 401 Unauthorized",
			want:   false,
		},
		{
			name:   "claude not-logged-in copy",
			errMsg: "Not logged in · Please run /login",
			want:   false,
		},
		{
			name:   "revoked access token",
			errMsg: "API Error: 403 access token has been revoked",
			want:   false,
		},
		{
			// The tail sentence of Google's own message, without its opening
			// clause. Matching on a fragment of it ("access token") would catch
			// every provider that names the credential it rejected.
			name:   "google sentence tail alone is not enough",
			errMsg: "Expected OAuth 2 access token, login cookie or other valid authentication credential.",
			want:   false,
		},
		{
			// Another provider's UNAUTHENTICATED status must not be read as
			// Antigravity's — only the phrases above are agy wording.
			name:   "grpc unauthenticated from another backend",
			errMsg: `{"code":16,"status":"UNAUTHENTICATED","message":"token expired"}`,
			want:   false,
		},
		{
			name:   "empty error",
			errMsg: "",
			want:   false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := AntigravitySessionTokenExpired(tt.errMsg); got != tt.want {
				t.Errorf("AntigravitySessionTokenExpired(%q) = %v, want %v", tt.errMsg, got, tt.want)
			}
		})
	}
}

// TestAntigravityNotLoggedIn pins the CLI-side predicate. This is the half the
// first review of DENE-724 blocked on: it must be recognisable on its own, and
// it must not be reachable through the 401 predicate, so the two can carry
// different copy.
func TestAntigravityNotLoggedIn(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		errMsg string
		want   bool
	}{
		{
			name:   "agy notice verbatim",
			errMsg: dene724NotLoggedInError,
			want:   true,
		},
		{
			name:   "wrapped by the daemon's stderr tail",
			errMsg: "agy exited with: You are not logged into Antigravity. Run agy to sign in.",
			want:   true,
		},
		{
			// A blob carrying BOTH wordings is the overlap the classifier
			// resolves toward this predicate: the not-logged-in copy stays
			// correct when the run's token also expired, while the 401 copy
			// ("your login is still fine") would not.
			name:   "both wordings in one blob",
			errMsg: dene724AntigravityError + "\n" + dene724NotLoggedInError,
			want:   true,
		},

		// --- Negatives: other tools' logged-out copy must not match. ---
		{
			name:   "dene724 google 401 alone is not this predicate",
			errMsg: dene724AntigravityError,
			want:   false,
		},
		{
			name:   "claude not-logged-in copy",
			errMsg: "Not logged in · Please run /login",
			want:   false,
		},
		{
			// "not logged in" without the product name is not enough: every
			// CLI says something like it, and retiring another provider's
			// healthy conversation would cost the member their context.
			name:   "bare not-logged-in phrase",
			errMsg: "error: not logged in",
			want:   false,
		},
		{
			name:   "empty error",
			errMsg: "",
			want:   false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := AntigravityNotLoggedIn(tt.errMsg); got != tt.want {
				t.Errorf("AntigravityNotLoggedIn(%q) = %v, want %v", tt.errMsg, got, tt.want)
			}
		})
	}
}

// TestAntigravityPhraseGuardsMatchResumeQueryGuards asserts the Go predicates
// and the SQL guards in GetLastTaskSession / GetLastChatTaskSession agree on
// every phrase. They are two independent implementations of one rule: the
// daemon classifier reads the predicates, and rows written by a daemon too old
// to carry it are caught by the ILIKE guards — so a phrase added on one side
// only would leave that layer resuming a session the other considers dead.
//
// Each phrase is also asserted against AntigravityResumeUnsafe, the single
// predicate service.ResumeUnsafeFailure and the query comments point at: the
// SQL guards do not know which of the two reasons a row was classified with, so
// the combined predicate is what has to cover both.
func TestAntigravityPhraseGuardsMatchResumeQueryGuards(t *testing.T) {
	t.Parallel()

	cases := []struct {
		phrase  string
		matches func(string) bool
	}{
		{"request had invalid authentication credentials", AntigravitySessionTokenExpired},
		{"not logged into antigravity", AntigravityNotLoggedIn},
	}

	for _, tc := range cases {
		probe := "prefix " + tc.phrase + " suffix"
		if !tc.matches(probe) {
			t.Errorf("predicate does not match the SQL guard phrase %q — pkg/db/queries and pkg/taskfailure have drifted", tc.phrase)
		}
		if !AntigravityResumeUnsafe(probe) {
			t.Errorf("AntigravityResumeUnsafe does not cover SQL guard phrase %q — the SQL guards and service.ResumeUnsafeFailure would disagree", tc.phrase)
		}
	}
}
