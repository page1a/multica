package closeprotocol

import (
	"strings"
	"testing"
)

func TestCheckSedimentLanding(t *testing.T) {
	pr := SedimentLanding{Via: LandingPR, Remote: RemoteIdentity("git@github.com:O/R.git"), PRURL: "https://github.com/o/r/pull/7", PRBase: "kun", PRMerged: true}
	for name, tc := range map[string]struct {
		landing  SedimentLanding
		mainline string
		want     string // "" passes
	}{
		"local repo merged here": {SedimentLanding{Via: LandingLocal}, "main", ""},
		"no mainline":            {SedimentLanding{Via: LandingLocal}, " ", "缺 mainline"},
		"pushed":                 {SedimentLanding{Via: LandingPush, Remote: "github.com/o/r", RemoteHasCommits: true}, "main", ""},
		"unpushed":               {SedimentLanding{Via: LandingPush, Remote: "github.com/o/r"}, "main", "还没收到"},
		"local merge, remote":    {SedimentLanding{Via: LandingLocal, Remote: "github.com/o/r"}, "main", "本地合入不算数"},
		"pr merged":              {pr, "kun", ""},
		"pr not merged":          {func() SedimentLanding { l := pr; l.PRMerged = false; return l }(), "kun", "还没合入"},
		"pr other repo":          {func() SedimentLanding { l := pr; l.PRURL = "https://github.com/x/r/pull/7"; return l }(), "kun", "不在这个仓库"},
		"pr other base":          {pr, "main", "项目主线是 main"},
		"pr, path origin":        {func() SedimentLanding { l := pr; l.Remote = RemoteIdentity("/srv/r.git"); return l }(), "kun", "不在这个仓库"},
		"pr without remote":      {func() SedimentLanding { l := pr; l.Remote = ""; return l }(), "kun", "没有远端"},
		"no via":                 {SedimentLanding{Remote: "github.com/o/r"}, "main", "缺落点"},
	} {
		got := CheckSedimentLanding(tc.landing, tc.mainline)
		if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}
