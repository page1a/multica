package gitconn

import "testing"

func TestPersonalConnectionMatchesOnlyTheOwnersRepos(t *testing.T) {
	conn := Conn{
		Provider: "github", InstanceURL: "https://github.com",
		AccountLogin: "octo", Covers: []string{"octo", "acme"},
		Personal: true, OwnerID: "user-1",
	}
	own := Repo{Key: "github.com/acme/multica", Registrant: "user-1"}
	if !Matches(conn, own) {
		t.Fatal("owner's repo under a covered org should match")
	}
	other := Repo{Key: "github.com/acme/multica", Registrant: "user-2"}
	if Matches(conn, other) {
		t.Fatal("personal connection must not cover someone else's repo")
	}
	outside := Repo{Key: "github.com/other/multica", Registrant: "user-1"}
	if Matches(conn, outside) {
		t.Fatal("org outside covers must not match")
	}
}

func TestEmptyCoversMatchTheWholeInstance(t *testing.T) {
	conn := Conn{Provider: "gitlab", InstanceURL: "https://gitlab.example.com", AccountLogin: "root"}
	repo := Repo{Key: "gitlab.example.com/group/app", Registrant: "user-9"}
	if !Matches(conn, repo) {
		t.Fatal("legacy instance connection should cover every repo on the host")
	}
}

func TestAskMessageCarriesLinkAndCommand(t *testing.T) {
	got := AskMessage("github.com/acme/app", SettingsURL("https://multica.example", "acme", "github.com"), AddCommand("github.com"))
	for _, want := range []string{"github.com/acme/app", "https://multica.example/acme/settings?tab=git-connections", "multica connection add --from-gh"} {
		if !stringsContains(got, want) {
			t.Fatalf("message %q missing %q", got, want)
		}
	}
}

func TestFromResourceReadsGitHubURL(t *testing.T) {
	repo, ok := FromResource("github_repo", []byte(`{"url":"https://github.com/Acme/App.git"}`))
	if !ok || repo.Key != "github.com/acme/app" {
		t.Fatalf("got %+v ok=%v", repo, ok)
	}
}

func stringsContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (len(s) > 0 && contains(s, sub)))
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
