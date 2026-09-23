package daemon

import (
	"strings"
	"testing"
)

func TestPromptExplainsASparseCheckout(t *testing.T) {
	t.Parallel()
	out := BuildPrompt(Task{IssueID: "issue-1", CheckoutPaths: "apps/web, packages/core"}, "claude")
	for _, want := range []string{"apps/web", "packages/core", "multica repo sparse-add", "MULTICA_SPARSE_EXCLUDED.txt"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	plain := BuildPrompt(Task{IssueID: "issue-1"}, "claude")
	if strings.Contains(plain, "Sparse checkout") {
		t.Fatal("a task with no declaration was told the checkout is sparse")
	}
}
