package ghsnapshot

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
)

// baseChecksQuery reads the checks on the latest commit of a PR's base branch
// (DENE-892). The close gate compares a PR's red checks against it by name: a
// check that is already red on the base is not this PR's failure.
const baseChecksQuery = `query($owner:String!,$repo:String!,$number:Int!,$cursor:String){
  repository(owner:$owner,name:$repo){
    pullRequest(number:$number){
      baseRefName
      baseRef{target{... on Commit{
        oid
        statusCheckRollup{
          state
          contexts(first:100,after:$cursor){
            pageInfo{hasNextPage endCursor}
            nodes{
              __typename
              ... on CheckRun{name status conclusion detailsUrl}
              ... on StatusContext{context state targetUrl}
            }
          }
        }
      }}}
    }
  }
}`

// BaseChecks is the CI on the head of a PR's base branch.
type BaseChecks struct {
	Branch   string
	HeadSHA  string
	Contexts []CheckContext
}

// redConclusions matches the failed set ListPullRequestsByIssue counts.
var redConclusions = map[string]bool{
	"failure": true, "cancelled": true, "timed_out": true, "action_required": true,
	"startup_failure": true, "stale": true, "error": true,
}

// FailedNames lists, sorted and once each, the checks that finished red. A
// name reported by several workflows counts as red when any of them is red;
// a check still running is not red yet.
func (b *BaseChecks) FailedNames() []string {
	if b == nil {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, c := range b.Contexts {
		if c.Status != "completed" || !redConclusions[c.Conclusion] || seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		names = append(names, c.Name)
	}
	sort.Strings(names)
	return names
}

type graphqlBaseData struct {
	Repository struct {
		PullRequest *struct {
			BaseRefName string `json:"baseRefName"`
			BaseRef     *struct {
				Target struct {
					Oid               string         `json:"oid"`
					StatusCheckRollup *graphqlRollup `json:"statusCheckRollup"`
				} `json:"target"`
			} `json:"baseRef"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

// FetchBaseChecks runs baseChecksQuery, paginating contexts to completion.
func FetchBaseChecks(ctx context.Context, c *Client, installationID int64, owner, repo string, number int32) (*BaseChecks, error) {
	if !c.Enabled() {
		return nil, errors.New("ghsnapshot: client not configured")
	}
	base := &BaseChecks{}
	cursor := ""
	for page := 0; page < maxSnapshotContextPages; page++ {
		vars := map[string]any{"owner": owner, "repo": repo, "number": number, "cursor": nil}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		data, err := c.graphQL(ctx, installationID, baseChecksQuery, vars)
		if err != nil {
			return nil, err
		}
		var parsed graphqlBaseData
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, errors.New("ghsnapshot: malformed base branch data")
		}
		pr := parsed.Repository.PullRequest
		if pr == nil || pr.BaseRef == nil {
			return nil, errors.New("ghsnapshot: base branch not found")
		}
		target := pr.BaseRef.Target
		if page == 0 {
			base.Branch = pr.BaseRefName
			base.HeadSHA = target.Oid
		} else if target.Oid != base.HeadSHA {
			return nil, errors.New("ghsnapshot: base branch moved during pagination")
		}
		rollup := target.StatusCheckRollup
		if rollup == nil {
			return base, nil
		}
		for _, raw := range rollup.Contexts.Nodes {
			if cc, ok := normalizeNode(raw); ok {
				base.Contexts = append(base.Contexts, cc)
			}
		}
		if !rollup.Contexts.PageInfo.HasNextPage {
			return base, nil
		}
		next := rollup.Contexts.PageInfo.EndCursor
		if next == "" || next == cursor {
			return nil, errors.New("ghsnapshot: invalid check-context pagination cursor")
		}
		cursor = next
	}
	return nil, errors.New("ghsnapshot: check-context pagination exceeds page limit")
}

// FetchBaseChecks reads the base branch CI for one linked pull request.
func (m *Manager) FetchBaseChecks(ctx context.Context, installationID int64, owner, repo string, number int32) (*BaseChecks, error) {
	if !m.Enabled() {
		return nil, errors.New("github app is not configured")
	}
	return FetchBaseChecks(ctx, m.client, installationID, owner, repo, number)
}
