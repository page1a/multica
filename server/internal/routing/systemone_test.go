package routing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// systemOneProbe captures what the judge actually put on the wire. The request
// shape IS the contract with TypeSafe — a question sent with the wrong type or
// a criteria map the endpoint rejects fails at 422, which the breaker reads as
// an ordinary stumble and the settings section reports as "the model failed".
type systemOneProbe struct {
	path  string
	auth  string
	body  map[string]any
	calls int
}

func systemOneServer(t *testing.T, status int, reply string) (*httptest.Server, *systemOneProbe) {
	t.Helper()
	probe := &systemOneProbe{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe.calls++
		probe.path = r.URL.Path
		probe.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &probe.body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, probe
}

func systemOneTarget(url string) Target {
	return Target{Model: "jev-latest", BaseURL: url, APIKey: "apikey_test"}
}

func assignState() JudgeState {
	return JudgeState{
		Title:      "Rename a button",
		Status:     "todo",
		Candidates: []string{"strongest", "strong", "medium", "weak"},
	}
}

const assignReply = `{
  "model": "jev-1.13.0",
  "answers": {
    "executor_tier": {"type":"choice","choice":"weak","probabilities":{"weak":0.9},"confidence":0.86},
    "reviewer": {"type":"choice","choice":"seat","probabilities":{"seat":0.8},"confidence":0.78},
    "reviewer_tier": {"type":"choice","choice":"strong","probabilities":{"strong":0.7},"confidence":0.64}
  }
}`

func TestSystemOneAssignReadsTypedAnswers(t *testing.T) {
	srv, probe := systemOneServer(t, http.StatusOK, assignReply)

	v, err := SystemOneJudge{HTTP: srv.Client()}.Assign(context.Background(), systemOneTarget(srv.URL), assignState())
	if err != nil {
		t.Fatalf("assign: %v", err)
	}

	if probe.path != "/v1/systemone" {
		t.Errorf("the evaluation endpoint is /v1/systemone, got %q", probe.path)
	}
	if probe.auth != "Bearer apikey_test" {
		t.Errorf("credential is sent as a bearer token, got %q", probe.auth)
	}
	if probe.body["model"] != "jev-latest" {
		t.Errorf("the configured model must be sent, got %v", probe.body["model"])
	}
	questions, _ := probe.body["questions"].(map[string]any)
	for _, id := range []string{qExecutorTier, qReviewer, qReviewerTier} {
		q, ok := questions[id].(map[string]any)
		if !ok {
			t.Fatalf("question %q was not sent; the three todo-row questions go in one call", id)
		}
		if q["type"] != "choice" {
			// A bounded branch is the whole reason this endpoint is worth
			// using: a free-text answer would put us back to parsing prose.
			t.Errorf("question %q must be a choice, got %v", id, q["type"])
		}
		if _, ok := q["criteria"].(map[string]any); !ok {
			t.Errorf("question %q was sent without criteria; the endpoint rejects a choice without them", id)
		}
	}
	tiers, _ := questions[qExecutorTier].(map[string]any)["criteria"].(map[string]any)
	if len(tiers) != 4 {
		t.Errorf("the executor question must offer exactly the candidate tiers, got %v", tiers)
	}

	if v.ExecutorTier != "weak" || v.ExecutorConfidence != 0.86 {
		t.Errorf("executor answer not carried through: %+v", v)
	}
	if v.Reviewer != ReviewerSeat || v.ReviewerTier != "strong" {
		t.Errorf("reviewer answer not carried through: %+v", v)
	}
	if v.Reason == "" {
		t.Error("the decision comment needs a sentence; a System One model returns no prose, so this package must compose one")
	}
}

func TestSystemOneReviewerConfidenceTakesTheWeakerHalf(t *testing.T) {
	srv, _ := systemOneServer(t, http.StatusOK, assignReply)

	v, err := SystemOneJudge{HTTP: srv.Client()}.Assign(context.Background(), systemOneTarget(srv.URL), assignState())
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	// The reviewer slot carries the TIER, so being 0.78 sure a seat should
	// review says nothing on its own: writing it also depends on the 0.64
	// answer about which seat. Taking the branch confidence alone would push
	// a below-threshold tier into the slot.
	if v.ReviewerConfidence != 0.64 {
		t.Errorf("reviewer confidence must be the weaker of branch and tier, got %v", v.ReviewerConfidence)
	}
}

func TestSystemOneNonSeatReviewerNeedsNoTier(t *testing.T) {
	srv, _ := systemOneServer(t, http.StatusOK, `{"model":"jev-1.13.0","answers":{
		"executor_tier":{"type":"choice","choice":"strong","confidence":0.9},
		"reviewer":{"type":"choice","choice":"none","confidence":0.81}
	}}`)

	v, err := SystemOneJudge{HTTP: srv.Client()}.Assign(context.Background(), systemOneTarget(srv.URL), assignState())
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if v.Reviewer != ReviewerNone || v.ReviewerTier != "" {
		t.Errorf("a none verdict carries no tier: %+v", v)
	}
	if v.ReviewerConfidence != 0.81 {
		t.Errorf("a non-seat branch is gated on its own confidence alone, got %v", v.ReviewerConfidence)
	}
}

func TestSystemOneRejectsAnUnknownBranch(t *testing.T) {
	srv, _ := systemOneServer(t, http.StatusOK, `{"model":"jev-1.13.0","answers":{
		"executor_tier":{"type":"choice","choice":"strong","confidence":0.9},
		"reviewer":{"type":"choice","choice":"maybe","confidence":0.99}
	}}`)

	_, err := SystemOneJudge{HTTP: srv.Client()}.Assign(context.Background(), systemOneTarget(srv.URL), assignState())
	// An answer this code does not understand is a broken contract, not a
	// low-confidence answer: treating it as the latter would write a slot from
	// a reply nobody parsed.
	if !errors.Is(err, ErrJudgeUnavailable) {
		t.Fatalf("an unrecognised branch must be unavailable, got %v", err)
	}
}

func TestSystemOneSeatVerdictWithoutATierIsUnavailable(t *testing.T) {
	srv, _ := systemOneServer(t, http.StatusOK, `{"model":"jev-1.13.0","answers":{
		"executor_tier":{"type":"choice","choice":"strong","confidence":0.9},
		"reviewer":{"type":"choice","choice":"seat","confidence":0.9}
	}}`)

	_, err := SystemOneJudge{HTTP: srv.Client()}.Assign(context.Background(), systemOneTarget(srv.URL), assignState())
	if !errors.Is(err, ErrJudgeUnavailable) {
		t.Fatalf("a seat verdict with no tier cannot fill the slot, got %v", err)
	}
}

func TestSystemOneStatusReachesTheBreaker(t *testing.T) {
	for _, code := range []int{401, 402, 403, 429} {
		srv, _ := systemOneServer(t, code, `{"detail":"nope"}`)
		_, err := SystemOneJudge{HTTP: srv.Client()}.Assign(context.Background(), systemOneTarget(srv.URL), assignState())
		if !errors.Is(err, ErrJudgeUnavailable) {
			t.Fatalf("%d: expected an unavailable judge, got %v", code, err)
		}
		// The status has to survive wrapping: the breaker classifies it with
		// errors.As, and a status flattened into a message string would leave
		// a revoked key being re-dialled once per ticket forever.
		b := NewBreaker()
		b.Fail("ws", err)
		open, _, reason := b.Open("ws")
		if !open {
			t.Errorf("%d: must open the breaker on the first failure, reason %q", code, reason)
		}
	}
}

func TestSystemOneOrdinaryFailureDoesNotOpenTheBreakerAtOnce(t *testing.T) {
	// 422 means this package sent a request the endpoint did not like. It is a
	// real failure but not a credential fact, so it must spend the ordinary
	// failure budget rather than cooling routing down immediately.
	srv, _ := systemOneServer(t, http.StatusUnprocessableEntity, `{"detail":"bad question"}`)
	_, err := SystemOneJudge{HTTP: srv.Client()}.Assign(context.Background(), systemOneTarget(srv.URL), assignState())
	b := NewBreaker()
	b.Fail("ws", err)
	if open, _, _ := b.Open("ws"); open {
		t.Error("a 422 must not trip the breaker on its first occurrence")
	}
}

func TestSystemOneSendsNoRequestWithoutAModel(t *testing.T) {
	srv, probe := systemOneServer(t, http.StatusOK, assignReply)
	target := systemOneTarget(srv.URL)
	target.Model = ""

	_, err := SystemOneJudge{HTTP: srv.Client()}.Assign(context.Background(), target, assignState())
	if !errors.Is(err, ErrJudgeUnavailable) {
		t.Fatalf("expected unavailable, got %v", err)
	}
	if probe.calls != 0 {
		t.Error("an incomplete configuration must not produce an outbound request")
	}
}

func TestSystemOneUnblockWritesNoTierWhenTheCauseIsNotTier(t *testing.T) {
	srv, _ := systemOneServer(t, http.StatusOK, `{"model":"jev-1.13.0","answers":{
		"cause":{"type":"choice","choice":"human","confidence":0.72},
		"suggested_tier":{"type":"choice","choice":"strongest","confidence":0.4}
	}}`)

	a, err := SystemOneJudge{HTTP: srv.Client()}.Unblock(context.Background(), systemOneTarget(srv.URL), assignState())
	if err != nil {
		t.Fatalf("unblock: %v", err)
	}
	if a.Cause != "human" {
		t.Fatalf("cause not carried through: %+v", a)
	}
	// The blocked row writes nothing, and a suggested tier next to a "needs a
	// person" cause would read as an instruction to reassign.
	if a.SuggestedTier != "" {
		t.Errorf("a non-tier cause must carry no suggested tier, got %q", a.SuggestedTier)
	}
	if a.Reason == "" {
		t.Error("the advice comment is the whole output of the blocked row")
	}
}

func TestTargetSystemOneNeedsBothHalvesAndTheHost(t *testing.T) {
	cases := []struct {
		name   string
		target Target
		want   bool
	}{
		{"typesafe host", Target{BaseURL: "https://api.typesafe.ai", APIKey: "k"}, true},
		{"typesafe with a v1 path", Target{BaseURL: "https://api.typesafe.ai/v1", APIKey: "k"}, true},
		{"typesafe without a scheme", Target{BaseURL: "api.typesafe.ai", APIKey: "k"}, true},
		{"another provider", Target{BaseURL: "https://api.openai.com/v1", APIKey: "k"}, false},
		{"lookalike host", Target{BaseURL: "https://typesafe.ai.example.com", APIKey: "k"}, false},
		// A half-filled pair already means "use the deployment gateway", which
		// is OpenAI-compatible. Reporting it as System One would send a
		// deployment credential in a body that endpoint has never seen.
		{"no key", Target{BaseURL: "https://api.typesafe.ai"}, false},
		{"no endpoint", Target{APIKey: "k"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.target.SystemOne(); got != tc.want {
				t.Errorf("SystemOne() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSystemOneEndpointAcceptsWhatPeopleActuallyPaste(t *testing.T) {
	want := "https://api.typesafe.ai/v1/systemone"
	for _, in := range []string{
		"https://api.typesafe.ai",
		"https://api.typesafe.ai/",
		"https://api.typesafe.ai/v1",
		"https://api.typesafe.ai/v1/",
		"https://api.typesafe.ai/v1/systemone",
		"api.typesafe.ai",
	} {
		if got := systemOneEndpoint(in); got != want {
			t.Errorf("systemOneEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
	if got := systemOneModelsEndpoint("https://api.typesafe.ai/v1"); got != "https://api.typesafe.ai/v1/models" {
		t.Errorf("model list endpoint = %q", got)
	}
}

func TestListSystemOneModelsReadsTheirShape(t *testing.T) {
	// Not the OpenAI `{"data":[{"id"}]}` envelope. Parsing it as one returns an
	// empty list, which is what made the settings section look as though this
	// product did not support Jev at all.
	srv, _ := systemOneServer(t, http.StatusOK, `{"models":[
		{"name":"jev-latest","description":"latest"},
		{"name":" ","description":"blank"},
		{"name":"jev-preview"}
	]}`)

	models, err := ListSystemOneModels(context.Background(), srv.Client(), srv.URL, "apikey_test")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(models) != 2 || models[0] != "jev-latest" || models[1] != "jev-preview" {
		t.Errorf("model names not read: %v", models)
	}
}

// providerCall records which judge a dispatched call landed on.
type providerCall struct {
	name    string
	sawFrom *[]string
}

func (p providerCall) Assign(_ context.Context, _ Target, _ JudgeState) (Verdict, error) {
	*p.sawFrom = append(*p.sawFrom, p.name)
	return Verdict{ExecutorTier: "strong", Reviewer: ReviewerNone}, nil
}

func (p providerCall) Unblock(_ context.Context, _ Target, _ JudgeState) (Advice, error) {
	*p.sawFrom = append(*p.sawFrom, p.name)
	return Advice{Cause: "other"}, nil
}

func TestProviderJudgeRoutesByProtocol(t *testing.T) {
	var seen []string
	j := ProviderJudge{
		Chat:      providerCall{name: "chat", sawFrom: &seen},
		SystemOne: providerCall{name: "systemone", sawFrom: &seen},
	}

	if _, err := j.Assign(context.Background(), Target{Model: "jev-latest", BaseURL: "https://api.typesafe.ai", APIKey: "k"}, JudgeState{}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if _, err := j.Assign(context.Background(), Target{Model: "gpt-x", BaseURL: "https://api.openai.com/v1", APIKey: "k"}, JudgeState{}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	// The deployment gateway has no workspace pair at all and must stay on the
	// path it was born on.
	if _, err := j.Assign(context.Background(), Target{Model: "gpt-x"}, JudgeState{}); err != nil {
		t.Fatalf("assign: %v", err)
	}

	want := []string{"systemone", "chat", "chat"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("dispatch went %v, want %v", seen, want)
	}
}

func TestSystemOneJudgeIsUnavailableForAChatTarget(t *testing.T) {
	// Health asks the judge whether it has anywhere to send a request. The
	// System One judge answering "yes" for an OpenAI endpoint would report a
	// healthy section that every ticket then fails against.
	if (SystemOneJudge{}).Available(Target{Model: "gpt-x", BaseURL: "https://api.openai.com/v1", APIKey: "k"}) {
		t.Error("a chat target is not this judge's to answer for")
	}
	if !(SystemOneJudge{}).Available(Target{Model: "jev-latest", BaseURL: "https://api.typesafe.ai", APIKey: "k"}) {
		t.Error("a configured System One target is available")
	}
}

func TestSystemOneHonoursContextDeadline(t *testing.T) {
	// Held open by a channel rather than by the request context: httptest's
	// Close waits for outstanding handlers, and a handler parked on the
	// request context does not always return when the client walks away.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := SystemOneJudge{HTTP: srv.Client()}.Assign(ctx, systemOneTarget(srv.URL), assignState())
	if !errors.Is(err, ErrJudgeUnavailable) {
		t.Fatalf("a hung endpoint must become an unavailable judge, got %v", err)
	}
}
