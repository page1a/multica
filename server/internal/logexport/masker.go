package logexport

import (
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/pkg/redact"
)

// envValuePlaceholder replaces a literal environment value wherever it appears.
// Distinct from pkg/redact's pattern placeholders so a reader can tell "this
// was a known env value" from "this looked like a token".
const envValuePlaceholder = "[REDACTED ENV VALUE]"

// secretEnvNameFragments is the deny-list. An environment variable whose name
// contains any fragment is treated as secret regardless of the value's shape:
// a password with no "sk-" prefix and no entropy still must not travel in a
// bundle. Matching is case-insensitive on the name only.
//
// The list is deliberately broader than pkg/redact's key=value pattern, which
// only fires on a fixed set of full names and only when the value is glued to
// the name by `=`/`:`. Bundles are handed to AI and committed to git, so the
// failure mode of over-masking one variable is a slightly less useful log,
// while the failure mode of under-masking is a leaked credential.
var secretEnvNameFragments = []string{
	"TOKEN",
	"SECRET",
	"PASSWORD",
	"PASSWD",
	"PASSPHRASE",
	"APIKEY",
	"API_KEY",
	"ACCESS_KEY",
	"PRIVATE_KEY",
	"PRIVATEKEY",
	"CREDENTIAL",
	"COOKIE",
	"SIGNING",
	"ENCRYPTION",
	"LICENSE_KEY",
	"WEBHOOK",
	"CONNECTION_STRING",
	"AUTHORIZATION",
	"AUTH_TOKEN",
	"REFRESH_TOKEN",
	"BEARER",
	"ANTHROPIC_API",
	"OPENAI_API",
	"DATABASE_URL",
	"REDIS_URL",
	"AMQP_URL",
	"MONGO_URL",
	"DSN",
	"STRIPE",
	"SUPABASE",
	"SENTRY",
	"NPM_TOKEN",
}

// minMaskedValueLen keeps a deny-listed variable whose value is a flag-like
// stub ("1", "true", "on") from erasing that token from ordinary log text. A
// real credential is longer than this; a boolean is not.
const minMaskedValueLen = 4

// Masker applies pattern redaction and then masks the literal values of the
// runs' environment. Literal masking is what catches a value that carries no
// recognizable secret shape — the exact gap a pattern list cannot close.
type Masker struct {
	literals []string
}

// NewMasker builds a masker from an environment map. Only values whose name
// matches the deny-list and that are long enough to be real values become
// literals; everything else is left to the pattern rules.
func NewMasker(env map[string]string) Masker {
	if len(env) == 0 {
		return Masker{}
	}
	literals := make([]string, 0, len(env))
	for name, value := range env {
		if len(value) < minMaskedValueLen {
			continue
		}
		if !isSecretEnvName(name) {
			continue
		}
		literals = append(literals, value)
	}
	// Longest first so a value that contains a shorter one is replaced whole.
	// Without this, masking "abc" before "abcdef" leaves "def" behind.
	sortByLengthDesc(literals)
	return Masker{literals: literals}
}

// SecretLiterals exposes the selected literal values. Used by tests and by
// callers that want to assert the deny-list picked up a value.
func (m Masker) SecretLiterals() []string {
	out := make([]string, len(m.literals))
	copy(out, m.literals)
	return out
}

// Text masks one string: pattern rules first, then known env literals.
func (m Masker) Text(s string) string {
	if s == "" {
		return s
	}
	s = redact.Text(s)
	for _, lit := range m.literals {
		s = strings.ReplaceAll(s, lit, envValuePlaceholder)
	}
	return s
}

// Value masks a decoded JSON value recursively. Tool inputs nest, so a
// top-level-only pass would leave a credential inside a patch body untouched.
func (m Masker) Value(v any) any {
	return m.walk(v, 0)
}

func (m Masker) walk(v any, depth int) any {
	if v == nil {
		return nil
	}
	if depth >= maxRedactDepth {
		return truncatedPlaceholder
	}
	switch t := v.(type) {
	case string:
		return m.Text(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = m.walk(val, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = m.walk(val, depth+1)
		}
		return out
	default:
		return v
	}
}

func isSecretEnvName(name string) bool {
	upper := strings.ToUpper(name)
	for _, fragment := range secretEnvNameFragments {
		if strings.Contains(upper, fragment) {
			return true
		}
	}
	return false
}

func sortByLengthDesc(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && len(values[j]) > len(values[j-1]); j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// failureLinePattern spots a "clue" line in the transcript for the summary: a
// line that reads like a failure rather than ordinary progress output.
var failureLinePattern = regexp.MustCompile(`(?i)\b(error|failed|failure|fatal|exception|timeout|timed out|refused|denied|panic)\b`)

// LooksLikeFailureLine reports whether a transcript line is worth surfacing as
// a clue in the AI summary.
func LooksLikeFailureLine(s string) bool {
	return failureLinePattern.MatchString(s)
}
