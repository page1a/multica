package taskfailure

import "strings"

// transientCheckoutWitnesses are git's messages for a checkout write that lost
// a race or hit a passing fault on the host, lower-cased: a file in the new
// worktree that could not be written, an index that could not be reset, a ref
// or index lock somebody else still held. A deterministic refusal (a branch
// that already exists, a bad ref, a path already registered) prints none of
// them.
var transientCheckoutWitnesses = []string{
	"unable to write file",
	"could not reset index file",
	"unable to write new index file",
	"index.lock",
	".lock': file exists",
	"cannot lock ref",
}

// TransientCheckout reports whether a failure text is git failing to finish a
// checkout for a passing reason. The daemon retries such a `git worktree add`
// in place, and the server treats an environment_prepare_failed carrying one
// of these as retryable instead of final (DENE-1339).
//
// DENE-1312 is the case that opened it: the tarot repo's worktree failed on
// eight PNGs ("unable to write file …", then "Could not reset index file to
// revision 'HEAD'"), the run was not retried, and the issue sat in todo with
// nobody driving it. A sibling on the same machine built its worktree fine 43
// seconds later.
func TransientCheckout(text string) bool {
	lower := strings.ToLower(text)
	for _, witness := range transientCheckoutWitnesses {
		if strings.Contains(lower, witness) {
			return true
		}
	}
	return false
}

// RetryableEnvironmentPrepare reports whether an environment_prepare_failed
// row is the passing kind that a later attempt can clear. Every other
// environment_prepare_failed stays final: a full disk or a denied permission
// fails the same way on the next attempt.
func RetryableEnvironmentPrepare(reason, text string) bool {
	return reason == string(ReasonEnvironmentPrepareFailed) && TransientCheckout(text)
}
