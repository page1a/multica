package handler

import (
	"context"
	"encoding/json"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
	"strings"
	"testing"
	"time"
)

// DENE-1678: an executor sending merged, already-reviewed work to review does
// not wait for an acceptance seat. The ticket lands in done and says why.
func TestCloseInReviewSkipsReviewWhenMergedAndReviewed(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for _, tc := range []struct {
		name   string
		seed   func(t *testing.T, issueID, prURL string)
		reason string
	}{
		{"github approve", func(t *testing.T, _, prURL string) {
			if _, err := testPool.Exec(context.Background(),
				`UPDATE github_pull_request SET approved_by = 'octo', approved_at = now(), approved_head_sha = head_sha WHERE html_url = $1`, prURL); err != nil {
				t.Fatalf("approve: %v", err)
			}
		}, "octo 在 GitHub 上批准过"},
		{"acceptance seat verdict", func(t *testing.T, issueID, _ string) {
			setMemberReviewer(t, issueID)
			seatPassHTTP(t, issueID)
		}, "在票上给过审查通过"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue := createIssueHTTP(t, "review skip "+tc.name, "in_progress")
			agentID := handlerTestAgentID(t)
			taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
			prURL := seedOpenPullForIssue(t, issue.ID, 167801)
			if _, err := testPool.Exec(context.Background(),
				`UPDATE github_pull_request SET state = 'merged', merged_at = now() WHERE html_url = $1`, prURL); err != nil {
				t.Fatalf("merge: %v", err)
			}
			tc.seed(t, issue.ID, prURL)

			w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
				"outcome":  "in_review",
				"evidence": "PR: " + prURL + "\n已合入。",
			})
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			var resp CloseIssueResponse
			if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp.Status != "done" {
				t.Fatalf("response status = %q, want done", resp.Status)
			}
			if got := issueStatusDirect(t, issue.ID); got != "done" {
				t.Fatalf("status = %s, want done", got)
			}
			if !strings.Contains(strings.Join(resp.Warnings, "\n"), "跳过验收席") {
				t.Fatalf("warnings should say acceptance was skipped, got %v", resp.Warnings)
			}
			if got := issueMetaString(t, issue.ID, "review_skip"); !strings.Contains(got, tc.reason) {
				t.Fatalf("review_skip = %q, want reason containing %q", got, tc.reason)
			}
		})
	}
}

// Merged but not reviewed in a way that counts: the ticket still goes to
// review. Covers the DENE-1678 review counterexamples — a pass by someone who
// is not the acceptance seat, and an approval of another PR on the ticket.
func TestCloseInReviewKeepsReviewWithoutQualifyingReview(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for _, tc := range []struct {
		name string
		seed func(t *testing.T, issueID, prURL string)
	}{
		{"only merged", func(t *testing.T, _, _ string) {}},
		{"pass by a non-seat", func(t *testing.T, issueID, _ string) {
			seedComment(t, issueID, time.Now().Add(-time.Minute), "verdict: pass", nil)
		}},
		{"another PR approved", func(t *testing.T, issueID, _ string) {
			old := seedOpenPullForIssue(t, issueID, 167899)
			if _, err := testPool.Exec(context.Background(),
				`UPDATE github_pull_request SET state = 'merged', merged_at = now(), approved_by = 'octo', approved_at = now(), approved_head_sha = head_sha WHERE html_url = $1`, old); err != nil {
				t.Fatalf("approve old PR: %v", err)
			}
		}},
		// A pass written before heads were recorded cannot say what it saw.
		{"seat pass with no recorded head", func(t *testing.T, issueID, _ string) {
			setMemberReviewer(t, issueID)
			seedComment(t, issueID, time.Now(), "verdict: pass", nil)
		}},
		{"approval of an earlier head", func(t *testing.T, _, prURL string) {
			if _, err := testPool.Exec(context.Background(),
				`UPDATE github_pull_request SET approved_by = 'octo', approved_at = now(), approved_head_sha = 'sha0000001' WHERE html_url = $1`, prURL); err != nil {
				t.Fatalf("approve: %v", err)
			}
		}},
		{"approval with no head", func(t *testing.T, _, prURL string) {
			if _, err := testPool.Exec(context.Background(),
				`UPDATE github_pull_request SET approved_by = 'octo', approved_at = now() WHERE html_url = $1`, prURL); err != nil {
				t.Fatalf("approve: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue := createIssueHTTP(t, "review skip negative "+tc.name, "in_progress")
			agentID := handlerTestAgentID(t)
			taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
			prURL := seedOpenPullForIssue(t, issue.ID, 167802)
			if _, err := testPool.Exec(context.Background(),
				`UPDATE github_pull_request SET state = 'merged', merged_at = now() WHERE html_url = $1`, prURL); err != nil {
				t.Fatalf("merge: %v", err)
			}
			tc.seed(t, issue.ID, prURL)
			w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
				"outcome": "in_review", "evidence": "PR: " + prURL,
			})
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			if got := issueStatusDirect(t, issue.ID); got != "in_review" {
				t.Fatalf("status = %s, want in_review", got)
			}
			if got := issueMetaString(t, issue.ID, "review_skip"); got != "" {
				t.Fatalf("review_skip = %q, want none", got)
			}
		})
	}
}

// DENE-1678 review F3: the seat passes the PR at H1, the executor pushes H2
// to the same PR and merges it. The old pass never saw H2, so the ticket
// still goes to review; once the seat passes again at H2, it skips.
func TestCloseInReviewSkipNeedsReviewOfMergedHead(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	for _, tc := range []struct {
		name       string
		repassAtH2 bool
		want       string
	}{
		{"pass at H1, H2 merged", false, "in_review"},
		{"passed again at H2", true, "done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue := createIssueHTTP(t, "review skip head "+tc.name, "in_progress")
			agentID := handlerTestAgentID(t)
			taskID := insertIssueTaskWithStatus(t, agentID, issue.ID, "running")
			prURL := seedOpenPullForIssue(t, issue.ID, 167803)
			setMemberReviewer(t, issue.ID)
			seatPassHTTP(t, issue.ID)
			if _, err := testPool.Exec(context.Background(),
				`UPDATE github_pull_request SET head_sha = 'shaH2000002' WHERE html_url = $1`, prURL); err != nil {
				t.Fatalf("push H2: %v", err)
			}
			if tc.repassAtH2 {
				seatPassHTTP(t, issue.ID)
			}
			if _, err := testPool.Exec(context.Background(),
				`UPDATE github_pull_request SET state = 'merged', merged_at = now() WHERE html_url = $1`, prURL); err != nil {
				t.Fatalf("merge: %v", err)
			}
			w := closeIssueHTTP(t, issue.ID, agentID, taskID, map[string]any{
				"outcome": "in_review", "evidence": "PR: " + prURL,
			})
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			if got := issueStatusDirect(t, issue.ID); got != tc.want {
				t.Fatalf("status = %s, want %s", got, tc.want)
			}
		})
	}
}

// seatPassHTTP posts the test user's `verdict: pass` through the comment
// endpoint, the path that records the PR heads the pass reviewed.
func seatPassHTTP(t *testing.T, issueID string) {
	t.Helper()
	id := postCommentForTriggerPreviewTest(t, issueID, map[string]any{"content": "审过了\nverdict: pass"})
	var n int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM review_pass_head WHERE comment_id = $1`, id).Scan(&n); err != nil || n == 0 {
		t.Fatalf("pass comment %s recorded %d heads (err %v)", id, n, err)
	}
}

// setMemberReviewer makes the test user the issue's acceptance seat.
func setMemberReviewer(t *testing.T, issueID string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(),
		`UPDATE issue SET reviewer_type = 'member', reviewer_id = $2 WHERE id = $1`, issueID, testUserID); err != nil {
		t.Fatalf("set reviewer: %v", err)
	}
}

// The pull_request_review webhook keeps the commit an approval was given on,
// so a later push to the same PR leaves that approval behind (DENE-1678).
func TestPullRequestReviewWebhookRecordsApprovedHead(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	const installationID int64 = 99887799
	if _, err := testHandler.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
		WorkspaceID: parseUUID(testWorkspaceID), InstallationID: installationID, AccountLogin: "review-acct", AccountType: "User",
	}); err != nil {
		t.Fatalf("CreateGitHubInstallation: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM github_installation WHERE installation_id = $1`, installationID)
	})
	issue := createIssueHTTP(t, "review webhook head", "in_progress")
	prURL := seedOpenPullForIssue(t, issue.ID, 167804)
	body, _ := json.Marshal(map[string]any{
		"action":       "submitted",
		"review":       map[string]any{"state": "approved", "submitted_at": "2026-10-09T01:02:03Z", "commit_id": "sha167804", "user": map[string]any{"login": "octo"}},
		"pull_request": map[string]any{"number": 167804},
		"repository":   map[string]any{"name": "multica", "owner": map[string]any{"login": "multica-ai"}},
		"installation": map[string]any{"id": installationID},
	})
	testHandler.handlePullRequestReviewEvent(ctx, body)
	var by, head string
	if err := testPool.QueryRow(ctx,
		`SELECT coalesce(approved_by, ''), coalesce(approved_head_sha, '') FROM github_pull_request WHERE html_url = $1`, prURL).Scan(&by, &head); err != nil {
		t.Fatalf("read approval: %v", err)
	}
	if by != "octo" || head != "sha167804" {
		t.Fatalf("approval = %q at %q, want octo at sha167804", by, head)
	}
}
