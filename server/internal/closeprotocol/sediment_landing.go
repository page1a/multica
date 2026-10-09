package closeprotocol

import (
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/repoident"
)

// A chat sediment counts only once the project's main line holds it
// (DENE-1668). The CLI reads the facts from git and gh; this rule judges them,
// and the server runs the same rule on what the CLI sends, so neither side
// keeps its own copy.

// How a chat's sediment reached the main line.
const (
	// LandingLocal: a repository with no remote, merged here.
	LandingLocal = "local"
	// LandingPush: the commits are on the remote's main line already.
	LandingPush = "push"
	// LandingPR: a merged PR carried them.
	LandingPR = "pr"
)

// RemoteUnidentified stands for an origin whose URL names no repository
// (a filesystem path): it can be checked by git, never matched to a PR.
const RemoteUnidentified = "unidentified"

// SedimentLanding is what git and gh said about where the sediment landed.
type SedimentLanding struct {
	Via string `json:"via"`
	// Remote is origin's repository (repoident key), "" for a repository
	// that only lives on this machine.
	Remote string `json:"remote,omitempty"`
	// RemoteHasCommits: origin's main line, fetched just now, contains every
	// commit reported (LandingPush).
	RemoteHasCommits bool `json:"remote_has_commits,omitempty"`
	// The PR that carried the work (LandingPR).
	PRURL    string `json:"pr_url,omitempty"`
	PRBase   string `json:"pr_base,omitempty"`
	PRMerged bool   `json:"pr_merged,omitempty"`
}

// RemoteIdentity is the Remote a SedimentLanding carries for an origin URL:
// credentials and transport stripped, so the URL itself never leaves here.
func RemoteIdentity(originURL string) string {
	if strings.TrimSpace(originURL) == "" {
		return ""
	}
	if key := repoident.NormalizeURL(originURL); key != "" {
		return string(key)
	}
	return RemoteUnidentified
}

// PRRepository is the repository a PR URL belongs to, "" when the URL is not
// a PR link (…/owner/repo/pull/N, GitLab's …/-/merge_requests/N).
func PRRepository(prURL string) string {
	u := strings.TrimSpace(prURL)
	for _, marker := range []string{"/-/merge_requests/", "/pull/", "/pulls/"} {
		if i := strings.Index(u, marker); i > 0 {
			return string(repoident.NormalizeURL(u[:i]))
		}
	}
	return ""
}

// CheckSedimentLanding is the rule: the project's main line received the
// work. A repository with a remote counts only what the remote's main line
// holds — pushed there, or merged by a PR into that repository and branch;
// one with no remote counts the local merge. "" passes.
func CheckSedimentLanding(l SedimentLanding, mainline string) string {
	mainline = strings.TrimSpace(mainline)
	if mainline == "" {
		return "缺 mainline：确定不了项目主线就不能记沉淀"
	}
	if strings.TrimSpace(l.Remote) == "" {
		if l.Via != LandingLocal {
			return fmt.Sprintf("落点 %q 不成立：这个仓库没有远端，只能就地合进 %s", l.Via, mainline)
		}
		return ""
	}
	switch l.Via {
	case LandingPush:
		if !l.RemoteHasCommits {
			return fmt.Sprintf("远端的 %s 还没收到这些提交：推到远端 %s，或开 PR 合入后带 --pr 再执行一次", mainline, mainline)
		}
		return ""
	case LandingPR:
		if strings.TrimSpace(l.PRURL) == "" {
			return "缺 pr_url：走 PR 的沉淀要带上那条 PR"
		}
		if !l.PRMerged {
			return fmt.Sprintf("PR %s 还没合入：合进 %s 后再上报", l.PRURL, mainline)
		}
		if repo := PRRepository(l.PRURL); repo == "" || repo != l.Remote {
			return fmt.Sprintf("PR %s 不在这个仓库（%s）：沉淀要合进本仓库的主线", l.PRURL, l.Remote)
		}
		if strings.TrimSpace(l.PRBase) != mainline {
			return fmt.Sprintf("PR 合进的是 %s，项目主线是 %s：沉淀要合进主线", l.PRBase, mainline)
		}
		return ""
	case LandingLocal:
		return fmt.Sprintf("这个仓库有远端，本地合入不算数：推到远端 %s，或开 PR 合入后带 --pr 再执行一次", mainline)
	default:
		return fmt.Sprintf("缺落点：不认识 %q，用新版 `multica chat sediment` 上报", l.Via)
	}
}
