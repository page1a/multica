package quotarelay_test

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/quotarelay"
	"github.com/multica-ai/multica/server/internal/routing"
)

// Lives outside package quotarelay so this file can import routing.
// routing already imports quotarelay; an in-package test that imported
// routing would be an import cycle.
func TestPickFindsTheLiveGPTTiers(t *testing.T) {
	ladder := routing.DefaultLadder.TierKeys()
	for _, name := range []string{"孙悟饭", "特兰克斯", "克林"} {
		tier, ok := routing.DefaultLadder.TierOf(name)
		if !ok {
			t.Fatalf("%s is not on the ladder", name)
		}
		provider, ok := routing.DefaultLadder.ProviderOf(name)
		if !ok || provider != "openai" {
			t.Fatalf("%s provider = %q ok=%v, want openai", name, provider, ok)
		}
		onLadder := false
		for _, key := range ladder {
			if key == tier {
				onLadder = true
				break
			}
		}
		if !onLadder {
			t.Fatalf("%s tier %q is not a ladder key", name, tier)
		}
		failed := quotarelay.Seat{ID: name, Name: name, Tier: tier}
		peer := quotarelay.Seat{ID: "peer-" + name, Name: "同伴", Tier: tier, Eligible: true}
		got, ok := quotarelay.Pick(failed, []quotarelay.Seat{failed, peer}, ladder)
		if !ok || got.Seat.ID != peer.ID || got.SteppedDown {
			t.Fatalf("Pick(%s tier %s) = %+v ok=%v, want the same-tier peer", name, tier, got, ok)
		}
	}
	if _, ok := quotarelay.Pick(quotarelay.Seat{ID: "x", Name: "x", Tier: "not-a-tier"}, nil, ladder); ok {
		t.Fatal("a tier that is not on the ladder must not relay")
	}
}
