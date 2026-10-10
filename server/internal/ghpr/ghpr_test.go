package ghpr

import (
	"encoding/json"
	"testing"
)

func TestOwnerRepo(t *testing.T) {
	for _, tc := range []struct {
		in          string
		owner, repo string
		ok          bool
	}{
		{"https://github.com/jeff-kunkun/multica/pull/374", "jeff-kunkun", "multica", true},
		{"https://github.com/jeff-kunkun/multica", "", "", false},
		{"not a url", "", "", false},
	} {
		o, r, ok := ownerRepo(tc.in)
		if o != tc.owner || r != tc.repo || ok != tc.ok {
			t.Errorf("ownerRepo(%q) = %q %q %v", tc.in, o, r, ok)
		}
	}
}

func TestApplySnapshotMapsGateFields(t *testing.T) {
	raw := json.RawMessage(`[
		{"__typename":"CheckRun","name":"backend","status":"COMPLETED","conclusion":"FAILURE"},
		{"__typename":"CheckRun","name":"frontend","status":"COMPLETED","conclusion":"SUCCESS"},
		{"__typename":"CheckRun","name":"e2e","status":"IN_PROGRESS","conclusion":""},
		{"__typename":"StatusContext","context":"ci/circle","state":"SUCCESS"},
		{"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"CANCELLED"},
		{"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"SUCCESS"}
	]`)
	var pr PR
	applySnapshot(&pr, ghRow{MergeStateStatus: "CLEAN", StatusCheckRollup: raw})
	if pr.MergeableState == nil || *pr.MergeableState != "clean" {
		t.Fatalf("mergeable = %v", pr.MergeableState)
	}
	if pr.ChecksRollup == nil || *pr.ChecksRollup != "failure" {
		t.Fatalf("rollup = %v, want failure", pr.ChecksRollup)
	}
	if len(pr.FailedCheckNames) != 1 || pr.FailedCheckNames[0] != "backend" {
		t.Fatalf("failed = %v, want [backend] (cancelled lint is superseded by success)", pr.FailedCheckNames)
	}
	if pr.ChecksRunning != 1 {
		t.Fatalf("running = %d, want 1", pr.ChecksRunning)
	}
	if pr.ReadyToMerge() {
		t.Fatal("red checks must not be ready to merge")
	}

	var clean PR
	applySnapshot(&clean, ghRow{MergeStateStatus: "CLEAN", StatusCheckRollup: json.RawMessage("null")})
	if clean.ChecksRollup == nil || *clean.ChecksRollup != "" || !clean.ReadyToMerge() {
		t.Fatalf("no checks + clean = %+v, want ready", clean)
	}

	var dirty PR
	applySnapshot(&dirty, ghRow{MergeStateStatus: "DIRTY", StatusCheckRollup: json.RawMessage("null")})
	if dirty.ReadyToMerge() || dirty.MergeableState == nil || *dirty.MergeableState != "dirty" {
		t.Fatalf("dirty = %+v", dirty)
	}

	var broken PR
	applySnapshot(&broken, ghRow{MergeStateStatus: "CLEAN", StatusCheckRollup: json.RawMessage(`{"state":"SUCCESS"}`)})
	if broken.ChecksRollup != nil || broken.ReadyToMerge() {
		t.Fatalf("unrecognized rollup must not look green: %+v", broken)
	}
}

func TestApplyApproval(t *testing.T) {
	var pr PR
	applyApproval(&pr, nil)
	if pr.ApprovedBy != nil {
		t.Fatal("reviews not requested must stay unread")
	}
	applyApproval(&pr, json.RawMessage(`[{"author":{"login":"a"},"state":"COMMENTED"}]`))
	if pr.ApprovedBy == nil || *pr.ApprovedBy != "" {
		t.Fatalf("no approval = %v, want read and empty", pr.ApprovedBy)
	}
	applyApproval(&pr, json.RawMessage(`[{"author":{"login":"a"},"state":"COMMENTED"},{"author":{"login":"b"},"state":"APPROVED","submittedAt":"2026-10-09T01:02:03Z","commit":{"oid":"abc1234def"}}]`))
	if pr.ApprovedBy == nil || *pr.ApprovedBy != "b" || pr.ApprovedAt == nil || pr.ApprovedHead != "abc1234def" {
		t.Fatalf("approval = %v %v %q, want b with time and head", pr.ApprovedBy, pr.ApprovedAt, pr.ApprovedHead)
	}
}
