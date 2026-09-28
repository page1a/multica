package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAnalysisKeyFollowsTheSameRules pins the nested analysis key to the
// three rules the judge key follows: sealed on write, carried forward when
// the write does not mention it, stripped on the way out (DENE-923).
func TestAnalysisKeyFollowsTheSameRules(t *testing.T) {
	h := boxedHandler(t)

	first, ok := h.applyRoutingSecret(settingsWith(map[string]any{
		"enabled":  true,
		"api_key":  "sk-judge",
		"analysis": map[string]any{"enabled": true, "model": "gpt-x", "api_key": "sk-analysis"},
	}), nil)
	if !ok {
		t.Fatal("write refused")
	}
	stored, _ := json.Marshal(first)
	if strings.Contains(string(stored), "sk-analysis") || strings.Contains(string(stored), "sk-judge") {
		t.Fatalf("plaintext survived into storage: %s", stored)
	}
	judgeSealed, analysisSealed := storedRoutingSealedKeys(stored)
	if h.openRoutingKey(judgeSealed) != "sk-judge" || h.openRoutingKey(analysisSealed) != "sk-analysis" {
		t.Fatalf("keys did not round-trip: judge=%q analysis=%q", judgeSealed, analysisSealed)
	}

	// An unrelated save sends both blocks without either key.
	second, ok := h.applyRoutingSecret(settingsWith(map[string]any{
		"enabled":  true,
		"analysis": map[string]any{"enabled": true, "model": "gpt-y"},
	}), stored)
	if !ok {
		t.Fatal("second write refused")
	}
	nested := routingBlockOf(t, second)["analysis"].(map[string]any)
	if nested["api_key_enc"] != analysisSealed || nested["model"] != "gpt-y" {
		t.Fatalf("analysis key not carried forward: %#v", nested)
	}

	// An explicit clear drops only the analysis key.
	third, _ := h.applyRoutingSecret(settingsWith(map[string]any{
		"enabled":  true,
		"analysis": map[string]any{"enabled": true, "api_key": ""},
	}), stored)
	block := routingBlockOf(t, third)
	if _, present := block["analysis"].(map[string]any)["api_key_enc"]; present {
		t.Fatal("explicit clear kept the analysis key")
	}
	if block["api_key_enc"] != judgeSealed {
		t.Fatal("clearing the analysis key touched the judge key")
	}

	// Reads never carry either sealed value.
	var decoded map[string]any
	_ = json.Unmarshal(stored, &decoded)
	out, _ := json.Marshal(redactRoutingSettings(decoded))
	if strings.Contains(string(out), "api_key_enc") {
		t.Fatalf("redacted read still carries a sealed key: %s", out)
	}
}

// TestAnalysisOnlyKeyIsRedacted covers a block whose only key is the
// analysis one: the early return for "no sealed key" must not skip it.
func TestAnalysisOnlyKeyIsRedacted(t *testing.T) {
	h := boxedHandler(t)
	sealed, _ := h.sealRoutingKey("sk-analysis")
	out, _ := json.Marshal(redactRoutingSettings(settingsWith(map[string]any{
		"analysis": map[string]any{"api_key_enc": sealed},
	})))
	if strings.Contains(string(out), sealed) {
		t.Fatalf("analysis-only key leaked: %s", out)
	}
}
