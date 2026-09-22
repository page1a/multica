// @vitest-environment is a TS notion; this is the canonical layer for the
// settings contract. The three field names here are what the desktop settings
// section writes, so a change that breaks this test breaks every workspace
// already configured.
package routing

import (
	"math"
	"reflect"
	"testing"
)

func TestParseSettingsContract(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   string
		want  Settings
		state State
	}{
		{"empty column", "", Settings{}, StateOff},
		{"empty object", `{}`, Settings{}, StateOff},
		{"null routing block", `{"routing":null}`, Settings{}, StateOff},
		{"unrelated settings are ignored", `{"theme":"dark"}`, Settings{}, StateOff},
		{
			"full block",
			`{"routing":{"enabled":true,"model":"gpt-5.6-luna","confidence_threshold":0.85}}`,
			Settings{Enabled: true, Model: "gpt-5.6-luna", ConfidenceThreshold: 0.85},
			StateEnabled,
		},
		{
			"switch on, no model chosen",
			`{"routing":{"enabled":true}}`,
			Settings{Enabled: true},
			StateIncomplete,
		},
		{
			"model chosen but switch off",
			`{"routing":{"enabled":false,"model":"m"}}`,
			Settings{Model: "m"},
			StateOff,
		},
		// Unparseable settings must not start routing tickets.
		{"malformed json", `{"routing":`, Settings{}, StateOff},
		{"routing is not an object", `{"routing":"yes"}`, Settings{}, StateOff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseSettings([]byte(tc.raw))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseSettings() = %+v, want %+v", got, tc.want)
			}
			if s := got.State(); s != tc.state {
				t.Errorf("State() = %q, want %q", s, tc.state)
			}
		})
	}
}

func TestThresholdNeverOpensTheGate(t *testing.T) {
	// Every rejected value falls back to the default rather than to something
	// that would accept an unconfident answer.
	for _, v := range []float64{0, -1, 1.5, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := (Settings{ConfidenceThreshold: v}).Threshold(); got != DefaultConfidenceThreshold {
			t.Errorf("Threshold() with %v = %v, want %v", v, got, DefaultConfidenceThreshold)
		}
	}
	if got := (Settings{ConfidenceThreshold: 0.5}).Threshold(); got != 0.5 {
		t.Errorf("Threshold() = %v, want 0.5", got)
	}
}

func TestOnlyEnabledStateIsActive(t *testing.T) {
	for _, s := range []State{StateOff, StateIncomplete, StateIneffective} {
		if s.Active() {
			t.Errorf("%q reports active; only %q may", s, StateEnabled)
		}
	}
	if !StateEnabled.Active() {
		t.Error("StateEnabled must be active")
	}
}
