package execenv

import "time"

// worktreeAddRetryDelays is the wait before each further `git worktree add`
// attempt after git failed on a passing fault (taskfailure.TransientCheckout).
// Two retries inside the repository lock: long enough for a sibling's lock or
// I/O burst to clear, short enough that a fault that does not pass hands back
// to the server's own retry quickly (DENE-1339).
var worktreeAddRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second}

// sleepBeforeWorktreeRetry is time.Sleep, swappable in tests so a test can
// release an injected lock at exactly the moment a real sibling would.
var sleepBeforeWorktreeRetry = time.Sleep
