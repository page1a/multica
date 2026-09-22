package routing

import "testing"

// Canonical layer for project -> direction resolution (DENE-706). Route tests
// keep one wiring case per outcome and point here for the matrix.
func TestResolveDirection(t *testing.T) {
	l := DefaultLadder.WithProjects(map[string]string{
		"game-*":      "游戏",
		"game-docs":   "自媒体",
		"Multica 魔改":  GenericDirection,
		"blank":       "",
		"typo":        "游戲",
		"game-relay":  "出海",
		" Tarot ":     "出海",
		"game-relay*": "学术",
	})
	for _, tc := range []struct {
		project string
		want    DirectionMatch
	}{
		{"", DirectionMatch{}},
		{"never-heard-of-it", DirectionMatch{}},
		{"game-new-vendor", DirectionMatch{Direction: "游戏", Known: true}},
		// Exact beats prefix; the workspace row beats the shipped one.
		{"game-docs", DirectionMatch{Direction: "自媒体", Known: true}},
		{"game-relay", DirectionMatch{Direction: "出海", Known: true}},
		// Longest prefix wins.
		{"game-relay-eu", DirectionMatch{Direction: "学术", Known: true}},
		// Shipped default still applies where the workspace said nothing.
		{"game", DirectionMatch{Direction: "游戏", Known: true}},
		{"TAROT", DirectionMatch{Direction: "出海", Known: true}},
		{"multica 魔改", DirectionMatch{Known: true}},
		{"blank", DirectionMatch{Known: true}},
		{"typo", DirectionMatch{Invalid: "游戲"}},
	} {
		if got := l.ResolveDirection(tc.project); got != tc.want {
			t.Errorf("ResolveDirection(%q) = %+v, want %+v", tc.project, got, tc.want)
		}
	}
}

func TestWithProjectsLeavesTheShippedLadderAlone(t *testing.T) {
	before := len(DefaultLadder.Projects)
	_ = DefaultLadder.WithProjects(map[string]string{"x": "游戏"})
	if len(DefaultLadder.Projects) != before {
		t.Fatal("WithProjects mutated the shared default ladder")
	}
}
