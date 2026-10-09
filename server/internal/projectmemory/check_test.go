package projectmemory

import (
	"reflect"
	"testing"
)

func TestMatchFilesFindsEachSlotsFiles(t *testing.T) {
	files := []string{"./CONTEXT.md", "apps/x/AGENTS.md", "AGENTS.md", "docs/adr/0002-b.md", "docs/adr/0001-a.md",
		"docs/README.md", "pkg/docs/README.md", "docs/evidence/INDEX.md", "docs/adr-notes.md", "MY-AGENTS.md"}
	for key, want := range map[string][]string{
		LocationAgents:   {"AGENTS.md", "apps/x/AGENTS.md"},
		LocationContext:  {"CONTEXT.md"},
		LocationADR:      {"docs/adr/0001-a.md", "docs/adr/0002-b.md"},
		LocationDocs:     {"docs/README.md", "pkg/docs/README.md"},
		LocationEvidence: {"docs/evidence/INDEX.md"},
	} {
		if got := MatchFiles(key, files); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", key, got, want)
		}
	}
	if got := MatchFiles("unknown", files); len(got) != 0 {
		t.Errorf("unknown key matched %v", got)
	}
}
