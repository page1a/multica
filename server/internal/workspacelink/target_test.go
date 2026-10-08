package workspacelink

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestTargetSlug(t *testing.T) {
	cases := map[string]string{
		"zongyoudiaominxianghaizhen": "zongyoudiaominxianghaizhen",
		"  Team-A  ":                 "team-a",
		"https://ai.ferryway.cc/zongyoudiaominxianghaizhen":   "zongyoudiaominxianghaizhen",
		"https://ai.ferryway.cc/zongyoudiaominxianghaizhen/":  "zongyoudiaominxianghaizhen",
		"https://ai.ferryway.cc/team-a/issues/DENE-1?tab=1#c": "team-a",
		"http://localhost:3000/team-a/settings":               "team-a",
		"ai.ferryway.cc/team-a/projects":                      "team-a",
		"localhost:3000/team-a":                               "team-a",
		"/team-a/issues":                                      "team-a",
		"team-a/":                                             "team-a",
		"team-a?x=1":                                          "team-a",
		"":                                                    "",
		"   ":                                                 "",
		"https://ai.ferryway.cc":                              "",
		"https://ai.ferryway.cc/":                             "",
	}
	for in, want := range cases {
		if got := TargetSlug(in); got != want {
			t.Errorf("TargetSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLookupRules(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	ref, err := w.svc.Lookup(ctx, w.source, owner(), "https://ai.example.test/"+w.viewerSlug+"/issues")
	if err != nil || ref.Slug != w.viewerSlug || ref.Name != "viewer" {
		t.Fatalf("lookup = %+v, %v", ref, err)
	}
	_, err = w.svc.Lookup(ctx, w.source, owner(), "nope-"+w.viewerSlug)
	wantStatus(t, err, 404)
	_, err = w.svc.Lookup(ctx, w.source, owner(), w.sourceSlug)
	wantStatus(t, err, 400)
	_, err = w.svc.Lookup(ctx, w.source, owner(), "https://ai.example.test/")
	wantStatus(t, err, 400)
	// Admins may look up (they can pull a link in); members may not.
	if _, err = w.svc.Lookup(ctx, w.source, admin(), w.viewerSlug); err != nil {
		t.Fatalf("admin lookup: %v", err)
	}
	_, err = w.svc.Lookup(ctx, w.source, member(), w.viewerSlug)
	wantStatus(t, err, 403)

	// Create reads the pasted link the same way.
	l, err := w.svc.Create(ctx, w.source, w.srcOwner, owner(), "https://ai.example.test/"+w.viewerSlug, []pgtype.UUID{w.shared})
	if err != nil || l.Target.Slug != w.viewerSlug {
		t.Fatalf("create from link = %+v, %v", l, err)
	}
}
