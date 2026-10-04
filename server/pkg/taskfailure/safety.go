package taskfailure

import "strings"

// safetyRefusalWitness is Anthropic's wording when its classifier blocks a
// request: "API Error: Opus 4.8's safeguards flagged this message. Our
// intentionally broad safeguards …". The account and the seat are fine; only
// this conversation is refused, and every Claude seat would refuse it again.
const safetyRefusalWitness = "safeguards flagged this message"

// IsSafetyRefusal reports a provider safety classifier refusing the run's
// content. The failure reason stays whatever Classify says; this is a separate
// witness so the taxonomy (and the offline SQL that mirrors it) is untouched.
func IsSafetyRefusal(rawError string) bool {
	return strings.Contains(strings.ToLower(rawError), safetyRefusalWitness)
}
