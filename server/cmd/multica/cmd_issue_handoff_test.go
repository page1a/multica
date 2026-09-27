package main

import (
	"strings"
	"testing"
)

func TestIssueHandoffCommandRegistration(t *testing.T) {
	cmd, _, err := issueCmd.Find([]string{"handoff"})
	if err != nil {
		t.Fatalf("find issue handoff: %v", err)
	}
	if cmd != issueHandoffCmd {
		t.Fatalf("found command = %q, want issue handoff", cmd.CommandPath())
	}
	for _, anchor := range []string{"--to reviewer", "--to dispatcher", "already has an active run", "issue close --outcome in_review"} {
		if !strings.Contains(cmd.Long, anchor) {
			t.Errorf("long help should carry the handoff contract (missing %q)", anchor)
		}
	}
	for _, name := range []string{"to", "output"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("issue handoff missing --%s", name)
		}
	}
}
