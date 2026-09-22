package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/vcs"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type webhookStatusCatalog struct {
	issuestatus.Querier
	reads      map[pgtype.UUID]int
	pointReads int
	fail       bool
}

func (c *webhookStatusCatalog) ListIssueStatusEntries(ctx context.Context, arg db.ListIssueStatusEntriesParams) ([]db.IssueStatus, error) {
	c.reads[arg.WorkspaceID]++
	if c.fail {
		return nil, errors.New("test catalog unavailable")
	}
	return c.Querier.ListIssueStatusEntries(ctx, arg)
}

func (c *webhookStatusCatalog) GetIssueStatusEntryByKey(ctx context.Context, arg db.GetIssueStatusEntryByKeyParams) (db.IssueStatus, error) {
	c.pointReads++
	if c.fail {
		return db.IssueStatus{}, errors.New("test catalog unavailable")
	}
	return c.Querier.GetIssueStatusEntryByKey(ctx, arg)
}

// Exercise both real mirror paths, including their persisted PR close gate.
// Signature parsing is covered by the existing provider webhook suites.
//
// Catalog READS are asserted by TestWebhookStatusResolverCatalogReads, not
// here. Counting them here cannot work: an issue that advances runs through
// notifyWaitersOfIssueDone, which builds its own Resolver and reads the
// catalog again, so the total is (1 + the number of issues this delivery
// advanced) — a number this test would have to restate every time that
// unrelated path changes. What the close check costs is measured on its own.
func TestWebhookStatusResolver(t *testing.T) {
	ctx := context.Background()
	for _, provider := range []string{"github", "forgejo"} {
		t.Run(provider, func(t *testing.T) {
			for _, tc := range []struct {
				name           string
				statuses, want []string
				fail           bool
			}{
				{"custom", []string{"approved", "dropped", "review", "approved", "done"}, []string{"approved", "dropped", "done", "approved", "done"}, false},
				{"builtins", []string{"done", "cancelled", "in_review"}, []string{"done", "cancelled", "done"}, false},
				// An unresolved key keeps the old nonterminal-gate behavior. This
				// optimization must not introduce a new close-policy decision.
				{"unknown", []string{"missing", "missing"}, []string{"done", "done"}, false},
				{"unavailable", []string{"review", "review"}, []string{"done", "done"}, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					catalog := &webhookStatusCatalog{Querier: testHandler.Queries, reads: map[pgtype.UUID]int{}, fail: tc.fail}
					h := *testHandler
					h.IssueStatusCatalog = catalog
					// The same key is terminal in one workspace and nonterminal in
					// the other; a process-wide resolver would close the wrong set.
					for workspace := 0; workspace < 2; workspace++ {
						ws := dbfx.Workspace(t, "Webhook status resolver", fmt.Sprintf("resolver-%s-%s-%d", provider, tc.name, workspace), testutil.Cols{"issue_prefix": "RSL"})
						wsID := parseUUID(ws)
						fixture := testutil.New(testPool, ws, testUserID)
						for key, category := range map[string]string{"approved": "done", "dropped": "closed", "review": "started"} {
							if workspace == 1 && key == "approved" {
								category = "started"
							}
							cols := testutil.Cols{"workspace_id": ws, "key": key, "name": key, "category": category, "color": "#123456"}
							if key == "approved" {
								cols["archived_at"] = testutil.Raw("now()")
							}
							fixture.Insert(t, "issue_status", cols)
						}
						var ids, closing []string
						for i, status := range tc.statuses {
							ids = append(ids, fixture.Issue(t, "Resolver fixture", testutil.Cols{"status": status, "number": i + 1}))
							closing = append(closing, fmt.Sprintf("Closes RSL-%d", i+1))
						}
						// Mirroring creates rows outside the fixture builders.
						for _, table := range []string{"issue_pull_request", "issue_vcs_pull_request"} {
							fixture.Cleanup(t, "DELETE FROM "+table+" WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1)", ws)
						}
						fixture.Cleanup(t, "DELETE FROM github_pull_request WHERE workspace_id = $1", ws)
						fixture.Cleanup(t, "DELETE FROM vcs_pull_request WHERE workspace_id = $1", ws)
						mirror := mirrorFixturePullRequest(ctx, t, &h, provider, fixture, ws, wsID, workspace, strings.Join(closing, "\n"))
						mirror()
						for i, id := range ids {
							want := tc.want[i]
							if workspace == 1 && tc.statuses[i] == "approved" {
								want = "done"
							}
							var status string
							fixture.QueryRow(t, "SELECT status FROM issue WHERE id = $1", id).Scan(&status)
							if status != want {
								t.Errorf("workspace %d issue %d status = %q, want %q", workspace, i, status, want)
							}
						}
						// A fresh delivery must not reuse even a successful prior
						// resolver. Reset one row onto a custom terminal key and replay.
						if tc.name == "custom" {
							fixture.Exec(t, "UPDATE issue SET status = 'dropped' WHERE id = $1", ids[0])
							mirror()
							var status string
							fixture.QueryRow(t, "SELECT status FROM issue WHERE id = $1", ids[0]).Scan(&status)
							if status != "dropped" {
								t.Errorf("replay changed terminal status to %q", status)
							}
						}
					}
					if catalog.pointReads != 0 {
						t.Errorf("per-key reads = %d, want 0", catalog.pointReads)
					}
				})
			}
		})
	}
}

// mirrorFixturePullRequest returns a delivery of one closed PR whose body
// closes every issue the fixture just created, driven through the provider's
// real mirror path. workspace is the loop index; the two providers are
// otherwise identical, which is what lets the read-count test below exercise
// both without restating the payload.
func mirrorFixturePullRequest(
	ctx context.Context,
	t *testing.T,
	h *Handler,
	provider string,
	fixture *testutil.Fixture,
	ws string,
	wsID pgtype.UUID,
	workspace int,
	body string,
) func() {
	t.Helper()
	const timestamp = "2026-09-08T00:00:00Z"
	if provider == "github" {
		p := &ghPullRequestPayload{}
		p.Action = "closed"
		p.Repository.Owner.Login, p.Repository.Name = "fixture", "resolver"
		p.PullRequest.Number, p.PullRequest.Title = 1, "Resolve linked issues"
		p.PullRequest.Body = body
		p.PullRequest.State, p.PullRequest.Merged = "closed", true
		p.PullRequest.HTMLURL = "https://github.test/fixture/resolver/pull/1"
		p.PullRequest.CreatedAt, p.PullRequest.UpdatedAt = timestamp, timestamp
		return func() {
			h.mirrorPullRequestForWorkspace(ctx, wsID, int64(91000+workspace), p, closeIntentPolicy{unrestricted: true})
		}
	}
	connID := fixture.Insert(t, "vcs_connection", testutil.Cols{"workspace_id": ws, "provider": provider, "instance_url": "https://forgejo.test", "account_login": "fixture", "access_token_encrypted": "unused", "webhook_secret_encrypted": "unused"})
	conn := db.VcsConnection{ID: parseUUID(connID), WorkspaceID: wsID, Provider: provider}
	ev := vcs.PullRequestEvent{Action: "closed", State: "merged", RepoOwner: "fixture", RepoName: "resolver", Number: 1, Title: "Resolve linked issues", Body: body, HTMLURL: "https://forgejo.test/fixture/resolver/pulls/1", CreatedAt: timestamp, UpdatedAt: timestamp}
	return func() { h.mirrorVCSPullRequest(ctx, conn, ev) }
}

// The close check resolves the workspace's status catalog at most ONCE per
// delivery, however many linked issues it re-evaluates, and once per delivery
// rather than once per process. That is the whole point of the workspace-scoped
// Resolver (f5f0b604f), and it is the only thing counted here.
//
// Every issue starts terminal, so nothing advances and the close check is the
// sole catalog reader. That is what makes this count attributable: an issue
// that does advance goes on to notifyWaitersOfIssueDone, which builds a
// Resolver of its own and reads the catalog again. Counting the two together
// is why the shared assertion in TestWebhookStatusResolver used to read 3 where
// it wanted 1 — and why it looked like cross-run state leaking in.
func TestWebhookStatusResolverCatalogReads(t *testing.T) {
	ctx := context.Background()
	for _, provider := range []string{"github", "forgejo"} {
		t.Run(provider, func(t *testing.T) {
			for _, tc := range []struct {
				name      string
				status    string
				wantReads int
			}{
				// Five linked issues, one catalog read: the resolver is reused
				// across the loop instead of loading per issue.
				{"custom terminal key", "approved", 1},
				// A built-in needs no catalog at all — the fast path the perf
				// change had to preserve.
				{"built-in key", "done", 0},
			} {
				t.Run(tc.name, func(t *testing.T) {
					catalog := &webhookStatusCatalog{Querier: testHandler.Queries, reads: map[pgtype.UUID]int{}}
					h := *testHandler
					h.IssueStatusCatalog = catalog
					ws := dbfx.Workspace(t, "Webhook status resolver reads", fmt.Sprintf("resolver-reads-%s-%s", provider, strings.ReplaceAll(tc.name, " ", "-")), testutil.Cols{"issue_prefix": "RSL"})
					wsID := parseUUID(ws)
					fixture := testutil.New(testPool, ws, testUserID)
					fixture.Insert(t, "issue_status", testutil.Cols{
						"workspace_id": ws, "key": "approved", "name": "approved",
						"category": "done", "color": "#123456", "archived_at": testutil.Raw("now()"),
					})

					const linked = 5
					var closing []string
					for i := 1; i <= linked; i++ {
						fixture.Issue(t, "Resolver read fixture", testutil.Cols{"status": tc.status, "number": i})
						closing = append(closing, fmt.Sprintf("Closes RSL-%d", i))
					}
					for _, table := range []string{"issue_pull_request", "issue_vcs_pull_request"} {
						fixture.Cleanup(t, "DELETE FROM "+table+" WHERE issue_id IN (SELECT id FROM issue WHERE workspace_id = $1)", ws)
					}
					fixture.Cleanup(t, "DELETE FROM github_pull_request WHERE workspace_id = $1", ws)
					fixture.Cleanup(t, "DELETE FROM vcs_pull_request WHERE workspace_id = $1", ws)

					mirror := mirrorFixturePullRequest(ctx, t, &h, provider, fixture, ws, wsID, 0, strings.Join(closing, "\n"))
					mirror()
					if got := catalog.reads[wsID]; got != tc.wantReads {
						t.Errorf("catalog reads for %d linked issues = %d, want %d", linked, got, tc.wantReads)
					}
					// A second delivery re-resolves from scratch: the resolver is
					// scoped to one mirror pass, not cached across webhooks.
					mirror()
					if got := catalog.reads[wsID]; got != tc.wantReads*2 {
						t.Errorf("catalog reads after replay = %d, want %d", got, tc.wantReads*2)
					}
					// And not through the per-key path, which would cost one read
					// per issue and is what this optimization replaced.
					if catalog.pointReads != 0 {
						t.Errorf("per-key reads = %d, want 0", catalog.pointReads)
					}
				})
			}
		})
	}
}
