package blockwait

import (
	"strings"
	"testing"
	"time"
)

func TestRejectAssociationWait(t *testing.T) {
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	bad := []string{
		"等待平台关联 PR",
		"等平台关联",
		"等关联 PR",
		"等待 PR 关联",
		"waiting for the platform to link the PR",
		"平台还没把 PR 关联上",
	}
	for _, text := range bad {
		_, err := Accept(nil, Input{WaitCondition: text, WaitTimeout: "2099-01-01T00:00:00Z"}, now)
		if err == nil || !strings.Contains(err.Error(), "--pr") {
			t.Fatalf("%q should be rejected with --pr, err=%v", text, err)
		}
	}
	for _, text := range []string{"等待合并", "关联 PR 已先合并", "等 CI 跑完"} {
		if _, err := Accept(nil, Input{WaitCondition: text, WaitTimeout: "2099-01-01T00:00:00Z"}, now); err != nil {
			t.Fatalf("%q should be allowed, err=%v", text, err)
		}
	}
	_, err := Accept(map[string]any{
		KeyWaitCondition: "等待平台关联 PR",
		KeyWaitTimeout:   "2099-01-01T00:00:00Z",
	}, Input{WakeAt: "2099-01-02T00:00:00Z"}, now)
	if err == nil || !strings.Contains(err.Error(), "--pr") {
		t.Fatal("stored association wait should be rejected")
	}
}
