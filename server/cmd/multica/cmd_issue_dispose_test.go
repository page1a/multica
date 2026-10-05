package main

import (
	"strings"
	"testing"
)

func TestIssueDisposeCommandRegistration(t *testing.T) {
	cmd, _, err := issueCmd.Find([]string{"dispose"})
	if err != nil {
		t.Fatalf("find issue dispose: %v", err)
	}
	if cmd != issueDisposeCmd {
		t.Fatalf("found command = %q, want issue dispose", cmd.CommandPath())
	}
	for _, anchor := range []string{"--action rerun", "--action reroute", "--action split --into", "--action cancel --reason", "already has a driver"} {
		if !strings.Contains(cmd.Long, anchor) {
			t.Errorf("long help should carry the dispose contract (missing %q)", anchor)
		}
	}
	for _, name := range []string{"action", "reason", "into", "output"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("issue dispose missing --%s", name)
		}
	}
}

func TestDriverCell(t *testing.T) {
	cases := map[string]map[string]any{
		"-":               {},
		"run":             {"driver": map[string]any{"kind": "run", "reason": "有运行在跑或在排队"}},
		"none 有执行人，但没有运行": {"driver": map[string]any{"kind": "none", "reason": "有执行人，但没有运行"}},
	}
	for want, issue := range cases {
		if got := driverCell(issue); got != want {
			t.Errorf("driverCell(%v) = %q, want %q", issue, got, want)
		}
	}
}
