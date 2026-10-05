package main

import (
	"strings"
	"testing"
)

func TestIssueStateCardCommandRegistration(t *testing.T) {
	for _, path := range [][]string{{"context"}, {"decision", "add"}, {"decision", "edit"}, {"decision", "rm"}} {
		cmd, _, err := issueCmd.Find(path)
		if err != nil || cmd.Name() != path[len(path)-1] {
			t.Fatalf("find issue %v: %v", path, err)
		}
		if cmd.Flags().Lookup("output") == nil {
			t.Errorf("issue %v missing --output", path)
		}
	}
	if issueContextCmd.Flags().Lookup("since") == nil {
		t.Error("issue context missing --since")
	}
	for _, anchor := range []string{"已拍板", "上一棒交代", "你上次之后的变化", "--thread"} {
		if !strings.Contains(issueContextCmd.Long, anchor) {
			t.Errorf("issue context help misses %q", anchor)
		}
	}
	if issueCloseCmd.Flags().Lookup("decision") == nil {
		t.Error("issue close missing --decision")
	}
	for _, name := range []string{"decision", "summary"} {
		if issueHandoffCmd.Flags().Lookup(name) == nil {
			t.Errorf("issue handoff missing --%s", name)
		}
	}
}
