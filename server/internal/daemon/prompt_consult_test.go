package daemon

import (
	"strings"
	"testing"
)

// The server renders a consult run's prompt (DENE-1721); the daemon sends it
// as is, never wrapped in the issue-run opener.
func TestBuildPromptBodySendsTheConsultPromptAsIs(t *testing.T) {
	got := buildPromptBody(Task{ID: "t-1", ConsultPrompt: "## Question\n\nWhich index?"}, "claude")
	if got != "## Question\n\nWhich index?" {
		t.Fatalf("prompt = %q", got)
	}
	if strings.Contains(got, "assigned issue") {
		t.Fatal("consult prompt must not carry the issue opener")
	}
	if meta, ok := gcMetaForTask(Task{ID: "t-1", WorkspaceID: "w", ConsultPrompt: "q"}); !ok || meta.TaskID != "t-1" {
		t.Fatalf("gc meta = %+v ok=%v, want task-keyed meta", meta, ok)
	}
}
