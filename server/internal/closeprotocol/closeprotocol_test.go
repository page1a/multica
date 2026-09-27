package closeprotocol

import (
	"errors"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/issuestatus"
)

const (
	commentID  = "01a0a4ae-46c3-7e9a-a90b-9ef7863cae7a"
	reviewerID = "1cbd7845-acbd-47d7-b0ea-582ec3d9f01f"
	parentID   = "9310ad38-468f-4d94-a68c-6f9eb4bf5d0a"
	memberID   = "c924599a-9548-4fc1-9146-a3645ecbb0c6"
	closedAt   = "2026-09-15T12:00:00Z"
)

func base(extra map[string]string) map[string]string {
	meta := map[string]string{
		KeyConclusion:        ConclusionDelivered,
		KeyStatus:            issuestatus.Done,
		KeyEvidenceCommentID: commentID,
		KeyNextOwnerType:     OwnerAgent,
		KeyNextOwnerID:       parentID,
		KeyWakeAction:        WakeStageDone,
		KeyWaitingOn:         "",
		KeyAt:                closedAt,
	}
	for k, v := range extra {
		meta[k] = v
	}
	return meta
}

func TestKeysAreTheEightCloseFields(t *testing.T) {
	want := []string{
		"close.conclusion",
		"close.status",
		"close.evidence_comment_id",
		"close.next_owner_type",
		"close.next_owner_id",
		"close.wake_action",
		"close.waiting_on",
		"close.at",
	}
	if len(Keys) != 8 {
		t.Fatalf("Keys len = %d, want 8", len(Keys))
	}
	for i, k := range want {
		if Keys[i] != k {
			t.Fatalf("Keys[%d] = %q, want %q", i, Keys[i], k)
		}
	}
}

func TestComplete_CommentOnlyIsNotAClose(t *testing.T) {
	if Complete(nil) || Complete(map[string]string{}) {
		t.Fatal("empty metadata must not count as a close")
	}
	partial := map[string]string{KeyConclusion: ConclusionDelivered, KeyStatus: issuestatus.Done}
	if Complete(partial) {
		t.Fatal("partial close.* keys must not count as a close")
	}
	if err := Validate(map[string]string{}, issuestatus.InProgress, "shipped, please review"); err == nil {
		t.Fatal("comment-only wrap-up must fail Validate")
	} else if rule(err) != "keys" {
		t.Fatalf("comment-only rule = %q, want keys", rule(err))
	}
}

// Four closing scenes from docs/kun/scheduling-close-protocol.md §2.4.
func TestValidate_FourClosingScenes(t *testing.T) {
	t.Run("done staged child (scene A)", func(t *testing.T) {
		meta := base(nil)
		if err := Validate(meta, issuestatus.Done, "delivery evidence; do not mention the parent assignee"); err != nil {
			t.Fatalf("scene A: %v", err)
		}
	})
	t.Run("in_review agent Reviewer (scene C)", func(t *testing.T) {
		meta := base(map[string]string{
			KeyConclusion:    ConclusionAwaitingReview,
			KeyStatus:        issuestatus.InReview,
			KeyNextOwnerType: OwnerAgent,
			KeyNextOwnerID:   reviewerID,
			KeyWakeAction:    WakeMention,
		})
		body := "[@代码审查-孙悟空](mention://agent/" + reviewerID + ") please review"
		if err := Validate(meta, issuestatus.InReview, body); err != nil {
			t.Fatalf("scene C: %v", err)
		}
	})
	t.Run("in_review human acceptance (scene D)", func(t *testing.T) {
		meta := base(map[string]string{
			KeyConclusion:    ConclusionAwaitingHuman,
			KeyStatus:        issuestatus.InReview,
			KeyNextOwnerType: OwnerMember,
			KeyNextOwnerID:   memberID,
			KeyWakeAction:    WakeNone,
		})
		if err := Validate(meta, issuestatus.InReview, "待人工测试: device playback"); err != nil {
			t.Fatalf("scene D: %v", err)
		}
	})
	t.Run("blocked (scene E)", func(t *testing.T) {
		meta := base(map[string]string{
			KeyConclusion:    ConclusionBlocked,
			KeyStatus:        issuestatus.Blocked,
			KeyNextOwnerType: OwnerMember,
			KeyNextOwnerID:   memberID,
			KeyWakeAction:    WakeNone,
			KeyBlockKind:     BlockDecision,
			KeyBlockAction:   "provide the blocking decision",
		})
		if err := Validate(meta, issuestatus.Blocked, "need a product decision"); err != nil {
			t.Fatalf("scene E: %v", err)
		}
	})
}

func TestValidate_Section61Rules(t *testing.T) {
	reviewBody := "[@reviewer](mention://agent/" + reviewerID + ")"

	tests := []struct {
		name        string
		meta        map[string]string
		issueStatus string
		body        string
		wantRule    string
	}{
		{
			name:        "close.status must equal issue.status",
			meta:        base(nil),
			issueStatus: issuestatus.InReview,
			wantRule:    "status_matches_issue",
		},
		{
			name: "stage_done requires done/cancelled and delivered",
			meta: base(map[string]string{
				KeyConclusion: ConclusionAwaitingReview,
				KeyStatus:     issuestatus.InReview,
				KeyWakeAction: WakeStageDone,
			}),
			issueStatus: issuestatus.InReview,
			wantRule:    "stage_done",
		},
		{
			name: "stage_done with in_review is not delivery",
			meta: base(map[string]string{
				KeyStatus:     issuestatus.InReview,
				KeyWakeAction: WakeStageDone,
			}),
			issueStatus: issuestatus.InReview,
			wantRule:    "stage_done",
		},
		{
			name: "mention requires agent/squad owner, id, and evidence mention",
			meta: base(map[string]string{
				KeyConclusion:    ConclusionAwaitingReview,
				KeyStatus:        issuestatus.InReview,
				KeyNextOwnerType: OwnerAgent,
				KeyNextOwnerID:   reviewerID,
				KeyWakeAction:    WakeMention,
			}),
			issueStatus: issuestatus.InReview,
			body:        "please review — no mention link",
			wantRule:    "mention",
		},
		{
			name: "mention with member owner is invalid",
			meta: base(map[string]string{
				KeyConclusion:    ConclusionAwaitingReview,
				KeyStatus:        issuestatus.InReview,
				KeyNextOwnerType: OwnerMember,
				KeyNextOwnerID:   memberID,
				KeyWakeAction:    WakeMention,
			}),
			issueStatus: issuestatus.InReview,
			body:        "[@kk](mention://member/" + memberID + ")",
			wantRule:    "mention",
		},
		{
			name: "awaiting_review requires wake_action=mention",
			meta: base(map[string]string{
				KeyConclusion:    ConclusionAwaitingReview,
				KeyStatus:        issuestatus.InReview,
				KeyNextOwnerType: OwnerAgent,
				KeyNextOwnerID:   reviewerID,
				KeyWakeAction:    WakeNone,
			}),
			issueStatus: issuestatus.InReview,
			wantRule:    "awaiting_review",
		},
		{
			name: "blocked requires blocked status",
			meta: base(map[string]string{
				KeyConclusion:    ConclusionBlocked,
				KeyStatus:        issuestatus.InReview,
				KeyWakeAction:    WakeNone,
				KeyNextOwnerType: OwnerMember,
				KeyNextOwnerID:   memberID,
				KeyBlockKind:     BlockDecision,
				KeyBlockAction:   "provide the blocking decision",
			}),
			issueStatus: issuestatus.InReview,
			wantRule:    "blocked",
		},
		{
			name: "waiting_on forbids done",
			meta: base(map[string]string{
				KeyWaitingOn: "DENE-196",
			}),
			issueStatus: issuestatus.Done,
			wantRule:    "waiting_on",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.meta, tt.issueStatus, tt.body)
			if err == nil {
				t.Fatal("expected error")
			}
			if rule(err) != tt.wantRule {
				t.Fatalf("rule = %q, want %q (%v)", rule(err), tt.wantRule, err)
			}
		})
	}

	t.Run("mention evidence with matching agent link passes", func(t *testing.T) {
		meta := base(map[string]string{
			KeyConclusion:    ConclusionAwaitingReview,
			KeyStatus:        issuestatus.InReview,
			KeyNextOwnerType: OwnerAgent,
			KeyNextOwnerID:   reviewerID,
			KeyWakeAction:    WakeMention,
		})
		if err := Validate(meta, issuestatus.InReview, reviewBody); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStatusMatchesIssue_DENE232Drift(t *testing.T) {
	// DENE-232 closed with close.status=in_review / awaiting_review, then the
	// issue moved to done. §6.1 requires equality; this is the automatic check
	// Stage 4 asked for, not a comment-only reminder.
	if StatusMatchesIssue(issuestatus.InReview, issuestatus.Done) {
		t.Fatal("close.status=in_review must not match issue.status=done")
	}
	if !StatusMatchesIssue(issuestatus.Done, issuestatus.Done) {
		t.Fatal("matching statuses must pass")
	}
	meta := base(map[string]string{
		KeyConclusion:    ConclusionAwaitingReview,
		KeyStatus:        issuestatus.InReview,
		KeyWakeAction:    WakeMention,
		KeyNextOwnerType: OwnerAgent,
		KeyNextOwnerID:   reviewerID,
	})
	body := "[@reviewer](mention://agent/" + reviewerID + ")"
	err := Validate(meta, issuestatus.Done, body)
	if err == nil {
		t.Fatal("Validate must reject DENE-232-style close.status drift")
	}
	if rule(err) != "status_matches_issue" {
		t.Fatalf("rule = %q, want status_matches_issue", rule(err))
	}
}

func TestValidate_AwaitingHumanDispatcher(t *testing.T) {
	meta := base(map[string]string{
		KeyConclusion:    ConclusionAwaitingHuman,
		KeyStatus:        issuestatus.InReview,
		KeyNextOwnerType: OwnerAgent,
		KeyNextOwnerID:   parentID,
		KeyWakeAction:    WakeMention,
		KeyWaitingOn:     "",
	})
	body := "[@dispatcher](mention://agent/" + parentID + ")"
	if err := Validate(meta, issuestatus.InReview, body); err == nil {
		t.Fatal("dispatcher awaiting_human without a named human must fail")
	} else if rule(err) != "awaiting_human" {
		t.Fatalf("rule = %q, want awaiting_human", rule(err))
	}

	meta[KeyWaitingOn] = "DENE-196"
	if err := Validate(meta, issuestatus.InReview, body); err != nil {
		t.Fatalf("waiting_on names the human wait: %v", err)
	}
}

func TestValidate_WaitingOnAllowsInReviewAndBlocked(t *testing.T) {
	meta := base(map[string]string{
		KeyConclusion:    ConclusionAwaitingReview,
		KeyStatus:        issuestatus.InReview,
		KeyNextOwnerType: OwnerAgent,
		KeyNextOwnerID:   reviewerID,
		KeyWakeAction:    WakeMention,
		KeyWaitingOn:     "DENE-196",
	})
	body := "[@r](mention://agent/" + reviewerID + ")"
	if err := Validate(meta, issuestatus.InReview, body); err != nil {
		t.Fatal(err)
	}
}

func TestValidate_AtMustBeUTC(t *testing.T) {
	meta := base(map[string]string{KeyAt: "2026-09-15T20:00:00+08:00"})
	if err := Validate(meta, issuestatus.Done, "delivery evidence"); err == nil {
		t.Fatal("non-UTC RFC3339 timestamp must fail")
	} else if rule(err) != "at" {
		t.Fatalf("rule = %q, want at", rule(err))
	}
}

func TestValidate_BlockerFields(t *testing.T) {
	t.Run("blocked record accepts each kind with an action", func(t *testing.T) {
		for _, kind := range []string{BlockDecision, BlockPermission, BlockExternal, BlockCapacity} {
			meta := base(map[string]string{
				KeyConclusion:    ConclusionBlocked,
				KeyStatus:        issuestatus.Blocked,
				KeyNextOwnerType: OwnerMember,
				KeyNextOwnerID:   memberID,
				KeyWakeAction:    WakeNone,
				KeyBlockKind:     kind,
				KeyBlockAction:   "take the next unblock action",
			})
			if err := Validate(meta, issuestatus.Blocked, "blocked"); err != nil {
				t.Fatalf("kind %s: %v", kind, err)
			}
		}
	})
	t.Run("dependency requires waiting_on", func(t *testing.T) {
		meta := base(map[string]string{KeyConclusion: ConclusionBlocked, KeyStatus: issuestatus.Blocked, KeyNextOwnerType: OwnerAgent, KeyNextOwnerID: reviewerID, KeyWakeAction: WakeNone, KeyBlockKind: BlockDependency, KeyBlockAction: "wait for the other issue"})
		if err := Validate(meta, issuestatus.Blocked, "blocked"); err == nil || rule(err) != "block_kind" {
			t.Fatalf("expected dependency waiting_on error, got %v", err)
		}
	})
	t.Run("legacy blocked record remains readable", func(t *testing.T) {
		meta := base(map[string]string{KeyConclusion: ConclusionBlocked, KeyStatus: issuestatus.Blocked, KeyNextOwnerType: OwnerMember, KeyNextOwnerID: memberID, KeyWakeAction: WakeNone})
		if err := ValidateLegacy(meta, issuestatus.Blocked, "legacy"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("new blocked record requires both blocker fields", func(t *testing.T) {
		meta := base(map[string]string{KeyConclusion: ConclusionBlocked, KeyStatus: issuestatus.Blocked, KeyNextOwnerType: OwnerMember, KeyNextOwnerID: memberID, KeyWakeAction: WakeNone})
		if err := Validate(meta, issuestatus.Blocked, "new blocked"); err == nil || rule(err) != "blocker" {
			t.Fatalf("expected missing blocker fields error, got %v", err)
		}
	})
	t.Run("action is bounded", func(t *testing.T) {
		meta := base(map[string]string{KeyConclusion: ConclusionBlocked, KeyStatus: issuestatus.Blocked, KeyNextOwnerType: OwnerMember, KeyNextOwnerID: memberID, KeyWakeAction: WakeNone, KeyBlockKind: BlockExternal, KeyBlockAction: strings.Repeat("x", 81)})
		if err := Validate(meta, issuestatus.Blocked, "blocked"); err == nil || rule(err) != "block_action" {
			t.Fatalf("expected action length error, got %v", err)
		}
	})
}

func TestValidate_MentionRequiresMarkdownMentionLink(t *testing.T) {
	meta := base(map[string]string{
		KeyConclusion:    ConclusionAwaitingReview,
		KeyStatus:        issuestatus.InReview,
		KeyNextOwnerType: OwnerAgent,
		KeyNextOwnerID:   reviewerID,
		KeyWakeAction:    WakeMention,
	})
	if err := Validate(meta, issuestatus.InReview, "mention://agent/"+reviewerID); err == nil {
		t.Fatal("bare mention URI must not satisfy evidence requirement")
	} else if rule(err) != "mention" {
		t.Fatalf("rule = %q, want mention", rule(err))
	}
}

func rule(err error) string {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Rule
	}
	return ""
}

// DENE-859: `multica issue close --outcome in_review` hands the ticket to the
// acceptance seat through routing, not through an executor @mention. The
// record says so with wake_action=route, which only makes sense on in_review.
func TestValidate_RouteWake(t *testing.T) {
	ok := base(map[string]string{
		KeyConclusion:    ConclusionAwaitingReview,
		KeyStatus:        issuestatus.InReview,
		KeyNextOwnerType: OwnerNone,
		KeyNextOwnerID:   "",
		KeyWakeAction:    WakeRoute,
	})
	if err := Validate(ok, issuestatus.InReview, "PR #1 绿了。"); err != nil {
		t.Fatalf("awaiting_review via route should validate without a mention, got %v", err)
	}

	human := base(map[string]string{
		KeyConclusion:    ConclusionAwaitingHuman,
		KeyStatus:        issuestatus.InReview,
		KeyNextOwnerType: OwnerMember,
		KeyNextOwnerID:   memberID,
		KeyWakeAction:    WakeRoute,
	})
	if err := Validate(human, issuestatus.InReview, "等人验收。"); err != nil {
		t.Fatalf("awaiting_human via route should validate, got %v", err)
	}

	wrongStatus := base(map[string]string{
		KeyNextOwnerType: OwnerNone,
		KeyNextOwnerID:   "",
		KeyWakeAction:    WakeRoute,
	})
	err := Validate(wrongStatus, issuestatus.Done, "done")
	var ce *Error
	if !errors.As(err, &ce) || ce.Rule != "route" {
		t.Fatalf("route on done should fail rule route, got %v", err)
	}
}
