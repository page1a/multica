// Package delivery answers "what did this issue ship?" for the close gate,
// the pull-request list, and an acceptance pass. The HTTP search and the
// database live with the handler; this package is the decision and the
// provider calls those two share.
package delivery

import (
	"fmt"
	"strings"
)

// GapKind is why a close cannot yet treat the issue as delivered.
type GapKind string

const (
	// GapNoConnection means no token and no GitHub App can see the repository.
	GapNoConnection GapKind = "no_connection"
	// GapNotFound means a connection exists (or an App is installed) and it
	// did not find a pull request whose title carries the issue key.
	GapNotFound GapKind = "not_found"
	// GapNotMerged means a pull request is linked and still open.
	GapNotMerged GapKind = "not_merged"
)

// Gap is the sentence and the next command a person or an agent can run.
type Gap struct {
	Kind        GapKind `json:"kind"`
	Message     string  `json:"message"`
	NextCommand string  `json:"next_command"`
}

// Pull is one pull or merge request the delivery lookup is willing to talk
// about. Mergeable and Checks use the close gate's vocabulary
// (clean/dirty/… and success/failure/pending).
type Pull struct {
	Provider  string
	Owner     string
	Repo      string
	Number    int32
	Title     string
	State     string
	URL       string
	Branch    string
	SHA       string
	Mergeable string
	Checks    string
	Author    string
}

// Open reports whether the pull still needs a merge before the issue can close.
func (p Pull) Open() bool {
	switch strings.ToLower(p.State) {
	case "open", "draft":
		return true
	default:
		return false
	}
}

// Situation is what the lookup knew after reading the registry and, when
// that was empty, asking the connections.
type Situation struct {
	Identifier string
	Pulls      []Pull
	// Queried is true when at least one token connection was asked and answered.
	Queried bool
	// Connected is true when a token connection or a GitHub App can see a
	// repository, even if the lookup did not call the provider (the App's
	// webhook is the registry).
	Connected bool
}

// GapFor turns a situation into the one reason a caller should show.
// A nil gap means the registry already holds a merged or closed delivery,
// or there is nothing to complain about because the caller did not ask
// about an empty result. Open pulls produce GapNotMerged.
func GapFor(s Situation) *Gap {
	ident := strings.TrimSpace(s.Identifier)
	if ident == "" {
		ident = "票号"
	}
	var open []Pull
	for _, p := range s.Pulls {
		if p.Open() {
			open = append(open, p)
		}
	}
	if len(s.Pulls) > 0 && len(open) == 0 {
		return nil
	}
	if len(open) > 0 {
		url := open[0].URL
		if url == "" {
			url = fmt.Sprintf("%s#%d", open[0].Repo, open[0].Number)
		}
		return &Gap{
			Kind:        GapNotMerged,
			Message:     "找到了但还没合并：" + url,
			NextCommand: mergeCommand(open[0]),
		}
	}
	prFlag := fmt.Sprintf("multica issue close %s --pr <PR或MR链接>", ident)
	if !s.Connected && !s.Queried {
		return &Gap{
			Kind:        GapNoConnection,
			Message:     "这个仓库还没接上，平台查不到 " + ident + " 的 PR 或 MR。",
			NextCommand: "在「设置 → 代码仓库」给这个仓库保存令牌，或 " + prFlag,
		}
	}
	return &Gap{
		Kind:        GapNotFound,
		Message:     "仓库已经接上，但没有标题带 " + ident + " 的 PR 或 MR。",
		NextCommand: "把 PR 标题改成带 " + ident + "，或 " + prFlag,
	}
}

func mergeCommand(p Pull) string {
	if p.URL == "" {
		return "先合并这个 PR 或 MR，再重跑 close"
	}
	switch strings.ToLower(p.Provider) {
	case "gitlab":
		return "glab mr merge " + p.URL + " --squash --yes"
	case "forgejo", "gitea":
		return "先在网页上合并 " + p.URL + "，再重跑 close"
	default:
		return "gh pr merge --squash " + p.URL
	}
}
