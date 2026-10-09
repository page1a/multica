package closeprotocol

import (
	"strings"
	"testing"
)

func TestCanonicalKnowledgeAuditNone(t *testing.T) {
	parsed, raw, err := CanonicalKnowledgeAudit(KnowledgeAudit{None: true})
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.None || len(parsed.Changes) != 0 || raw != `{"none":true}` {
		t.Fatalf("parsed = %+v raw = %s", parsed, raw)
	}
}

func TestCanonicalKnowledgeAuditChangeUsesChecklist(t *testing.T) {
	parsed, raw, err := CanonicalKnowledgeAudit(KnowledgeAudit{Changes: []KnowledgeChange{{
		Location: " agents ",
		Summary:  " 补了开张种子 ",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Changes) != 1 || parsed.Changes[0].Location != "agents" || parsed.Changes[0].Summary != "补了开张种子" {
		t.Fatalf("parsed = %+v", parsed)
	}
	if raw != `{"changes":[{"location":"agents","summary":"补了开张种子"}]}` {
		t.Fatalf("raw = %s", raw)
	}
}

func TestCanonicalKnowledgeAuditRejectsUnknownLocation(t *testing.T) {
	_, _, err := CanonicalKnowledgeAudit(KnowledgeAudit{Changes: []KnowledgeChange{{
		Location: "DESIGN.md",
		Summary:  "改了版式",
	}}})
	if err == nil || !strings.Contains(err.Error(), "不在项目记忆清单里") || !strings.Contains(err.Error(), "agents") {
		t.Fatalf("err = %v", err)
	}
}

func TestCanonicalKnowledgeAuditRejectsMissingAndMixed(t *testing.T) {
	if _, _, err := CanonicalKnowledgeAudit(KnowledgeAudit{}); err == nil || err.Error() != KnowledgeAuditRequiredMsg {
		t.Fatalf("missing = %v", err)
	}
	_, _, err := CanonicalKnowledgeAudit(KnowledgeAudit{
		None:    true,
		Changes: []KnowledgeChange{{Location: "context", Summary: "定了词"}},
	})
	if err == nil || !strings.Contains(err.Error(), "不能同时") {
		t.Fatalf("mixed = %v", err)
	}
}

func TestBindDeliveredFilesRecordsTheFilesThatWriteEachSlot(t *testing.T) {
	audit := KnowledgeAudit{Changes: []KnowledgeChange{{Location: "context", Summary: "词条"}, {Location: "adr", Summary: "新决定"}}}
	delivered := []string{"server/x.go", "CONTEXT.md", "docs/adr/0009-y.md", "./docs/adr/0010-z.md"}
	bound, err := BindDeliveredFiles(audit, &delivered)
	if err != nil {
		t.Fatal(err)
	}
	if got := bound.Changes[0].Files; len(got) != 1 || got[0] != "CONTEXT.md" {
		t.Fatalf("context files = %v", got)
	}
	if got := bound.Changes[1].Files; len(got) != 2 || got[0] != "docs/adr/0009-y.md" || got[1] != "docs/adr/0010-z.md" {
		t.Fatalf("adr files = %v", got)
	}
	if bound.Unverified {
		t.Fatal("a checked audit is not unverified")
	}
}

func TestBindDeliveredFilesRefusesASlotNoDeliveredFileWrites(t *testing.T) {
	audit := KnowledgeAudit{Changes: []KnowledgeChange{{Location: "agents", Summary: "新规则"}}}
	delivered := []string{"server/x.go", "docs/AGENTS-notes.md"}
	_, err := BindDeliveredFiles(audit, &delivered)
	if err == nil || !strings.Contains(err.Error(), "agents（AGENTS.md）") || !strings.Contains(err.Error(), "--knowledge-none") {
		t.Fatalf("err = %v", err)
	}
	empty := []string{}
	if _, err := BindDeliveredFiles(audit, &empty); err == nil {
		t.Fatal("an empty delivery cannot carry a memory change")
	}
}

func TestBindDeliveredFilesWithoutAListMarksUnverified(t *testing.T) {
	audit := KnowledgeAudit{Changes: []KnowledgeChange{{Location: "agents", Summary: "新规则"}}}
	bound, err := BindDeliveredFiles(audit, nil)
	if err != nil || !bound.Unverified {
		t.Fatalf("bound = %+v err = %v", bound, err)
	}
	none, err := BindDeliveredFiles(KnowledgeAudit{None: true}, nil)
	if err != nil || none.Unverified {
		t.Fatalf("无够格知识 needs no files: %+v %v", none, err)
	}
}

func TestStripKnowledgeEvidenceDropsCallerClaimedProof(t *testing.T) {
	claimed := KnowledgeAudit{Changes: []KnowledgeChange{{Location: "agents", Summary: "s", Files: []string{"AGENTS.md"}}}, Unverified: true}
	got := StripKnowledgeEvidence(claimed)
	if got.Unverified || got.Changes[0].Files != nil {
		t.Fatalf("got = %+v", got)
	}
}
