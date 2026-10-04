package taskfailure

import "testing"

func TestIsSafetyRefusal(t *testing.T) {
	cases := map[string]bool{
		"API Error: Opus 4.8's safeguards flagged this message. Our intentionally broad safeguards…": true,
		"API Error: 529 overloaded":     false,
		"Claude AI usage limit reached": false,
		"":                              false,
	}
	for in, want := range cases {
		if got := IsSafetyRefusal(in); got != want {
			t.Errorf("IsSafetyRefusal(%q) = %v, want %v", in, got, want)
		}
	}
}
