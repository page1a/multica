package taskfailure

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

var (
	fingerprintUUID      = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	fingerprintNumber    = regexp.MustCompile(`\b\d+\b`)
	fingerprintSpace     = regexp.MustCompile(`\s+`)
	deterministicFailure = regexp.MustCompile(`(?i)(replay|already exists|permission denied|access denied|eacces|enospc|no space left|read-only file system|disk full)`)
)

// Fingerprint returns a stable identifier for a failure kind and diagnostic shape.
func Fingerprint(reason, errorText string) string {
	normalized := strings.ToLower(strings.TrimSpace(errorText))
	normalized = fingerprintUUID.ReplaceAllString(normalized, "<uuid>")
	normalized = fingerprintNumber.ReplaceAllString(normalized, "<n>")
	normalized = fingerprintSpace.ReplaceAllString(normalized, " ")
	sum := sha256.Sum256([]byte(strings.TrimSpace(reason) + "\x00" + normalized))
	return hex.EncodeToString(sum[:])[:24]
}

// IsDeterministic reports error witnesses that normally reproduce until a
// person changes the input or the local environment. It is intentionally
// narrow so provider/network failures keep their existing retry budget.
func IsDeterministic(errorText string) bool {
	return deterministicFailure.MatchString(errorText)
}
