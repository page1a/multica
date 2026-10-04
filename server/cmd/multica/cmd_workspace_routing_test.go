package main

import (
	"github.com/spf13/cobra"
	"testing"
)

func newWorkspaceRoutingSetTestCmd() *cobra.Command {
	cmd := newRoutingProjectsTestCmd()
	for _, f := range []string{"source", "runtime", "model", "thinking", "continuation", "load", "usage-priority", "allow-upshift"} {
		cmd.Flags().String(f, "", "")
	}
	return cmd
}

func TestWorkspaceRoutingSetContinuationKeepsTheRest(t *testing.T) {
	var patched map[string]any
	routingProjectsServer(t, map[string]any{
		"routing": map[string]any{
			"enabled": true, "model": "jev-1",
			"analysis": map[string]any{"model": "a-1"},
		},
	}, &patched)

	cmd := newWorkspaceRoutingSetTestCmd()
	_ = cmd.Flags().Set("continuation", "on")
	if err := runWorkspaceRoutingSet(cmd, nil); err != nil {
		t.Fatalf("set: %v", err)
	}
	settings, _ := patched["settings"].(map[string]any)
	block, _ := settings["routing"].(map[string]any)
	if block["prefer_continuation"] != true {
		t.Fatalf("continuation not written: %v", block)
	}
	if block["enabled"] != true || block["model"] != "jev-1" {
		t.Fatalf("routing switch/model were not carried through: %v", block)
	}
	analysis, _ := block["analysis"].(map[string]any)
	if analysis["model"] != "a-1" {
		t.Fatalf("analysis block was not carried through: %v", block)
	}
}

func TestWorkspaceRoutingSetContinuationRejectsOtherValues(t *testing.T) {
	cmd := newWorkspaceRoutingSetTestCmd()
	_ = cmd.Flags().Set("continuation", "yes")
	if err := runWorkspaceRoutingSet(cmd, nil); err == nil {
		t.Fatal("want an error for --continuation yes")
	}
}

func TestRoutingViewReportsShadowByDefault(t *testing.T) {
	if got := routingView(nil); got["prefer_continuation"] != false || got["continuation_mode"] != "shadow" {
		t.Fatalf("default view = %v, want shadow", got)
	}
	if got := routingView(map[string]any{"prefer_continuation": true}); got["continuation_mode"] != "on" {
		t.Fatalf("on view = %v", got)
	}
	if got := routingView(nil); got["usage_priority"] != true || got["allow_upshift"] != false {
		t.Fatalf("default tier switches = %v", got)
	}
}

func TestWorkspaceRoutingSetTierSwitches(t *testing.T) {
	var patched map[string]any
	routingProjectsServer(t, map[string]any{"routing": map[string]any{"usage_priority": true}}, &patched)
	cmd := newWorkspaceRoutingSetTestCmd()
	_ = cmd.Flags().Set("usage-priority", "off")
	_ = cmd.Flags().Set("allow-upshift", "on")
	if err := runWorkspaceRoutingSet(cmd, nil); err != nil {
		t.Fatalf("set: %v", err)
	}
	settings, _ := patched["settings"].(map[string]any)
	block, _ := settings["routing"].(map[string]any)
	if block["usage_priority"] != false || block["allow_upshift"] != true {
		t.Fatalf("tier switches = %v", block)
	}
	if got := routingView(block); got["usage_priority"] != false || got["allow_upshift"] != true {
		t.Fatalf("view = %v", got)
	}
}

// DENE-1203: --load flips 负载分流 and leaves the 接着做 switch alone.
func TestWorkspaceRoutingSetLoadKeepsContinuation(t *testing.T) {
	var patched map[string]any
	routingProjectsServer(t, map[string]any{
		"routing": map[string]any{"enabled": true, "prefer_continuation": true},
	}, &patched)

	cmd := newWorkspaceRoutingSetTestCmd()
	_ = cmd.Flags().Set("load", "on")
	if err := runWorkspaceRoutingSet(cmd, nil); err != nil {
		t.Fatalf("set: %v", err)
	}
	settings, _ := patched["settings"].(map[string]any)
	block, _ := settings["routing"].(map[string]any)
	if block["prefer_idle"] != true || block["prefer_continuation"] != true {
		t.Fatalf("load not written or continuation lost: %v", block)
	}
	if got := routingView(block); got["load_mode"] != "on" || got["continuation_mode"] != "on" {
		t.Fatalf("view = %v", got)
	}
	if got := routingView(nil); got["prefer_idle"] != false || got["load_mode"] != "shadow" {
		t.Fatalf("default view = %v, want shadow", got)
	}

	bad := newWorkspaceRoutingSetTestCmd()
	_ = bad.Flags().Set("load", "yes")
	if err := runWorkspaceRoutingSet(bad, nil); err == nil {
		t.Fatal("want an error for --load yes")
	}
}
