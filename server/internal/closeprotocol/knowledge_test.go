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
