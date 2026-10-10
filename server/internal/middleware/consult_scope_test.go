package middleware

import "testing"

func TestConsultScopeIsReadOnly(t *testing.T) {
	if !isConsultReadOnlyScope(ConsultTaskKind) {
		t.Fatal("a consult token must be read-only")
	}
	for _, kind := range []string{"", "issue", "chat", "quick_create"} {
		if isConsultReadOnlyScope(kind) {
			t.Errorf("%q should not be read-only", kind)
		}
	}
}
