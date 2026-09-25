package main

import "testing"

func TestParseChatSessionRef(t *testing.T) {
	const id = "019ec09d-6222-722b-bdfa-427b105d80be"
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "bare id", raw: id, want: id},
		{name: "uppercase id", raw: "019EC09D-6222-722B-BDFA-427B105D80BE", want: id},
		{name: "path url", raw: "https://app.example/acme/chat/" + id, want: id},
		{name: "path with query", raw: "https://app.example/acme/chat/" + id + "?from=copy", want: id},
		{name: "legacy query", raw: "https://app.example/acme/chat?session=" + id, want: id},
		{name: "relative path", raw: "/acme/chat/" + id, want: id},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseChatSessionRef(tc.raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}

	if _, err := parseChatSessionRef("https://app.example/acme/issues/not-a-session"); err == nil {
		t.Fatal("expected an error for a url with no session id")
	}
}
