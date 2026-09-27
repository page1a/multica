package ghpr

import "testing"

func TestOwnerRepo(t *testing.T) {
	for _, tc := range []struct {
		in          string
		owner, repo string
		ok          bool
	}{
		{"https://github.com/jeff-kunkun/multica/pull/374", "jeff-kunkun", "multica", true},
		{"https://github.com/jeff-kunkun/multica", "", "", false},
		{"not a url", "", "", false},
	} {
		o, r, ok := ownerRepo(tc.in)
		if o != tc.owner || r != tc.repo || ok != tc.ok {
			t.Errorf("ownerRepo(%q) = %q %q %v", tc.in, o, r, ok)
		}
	}
}
