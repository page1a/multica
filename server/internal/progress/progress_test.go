package progress

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/parking"
)

func TestNormalizeTone(t *testing.T) {
	for in, want := range map[string]string{"": "", " Stuck ": ToneStuck, "done": ToneDone} {
		if got, ok := NormalizeTone(in); !ok || got != want {
			t.Errorf("NormalizeTone(%q) = %q, %v", in, got, ok)
		}
	}
	if _, ok := NormalizeTone("red"); ok {
		t.Error("unknown tone accepted")
	}
}

func TestForStatus(t *testing.T) {
	for status, want := range map[string]string{
		"done": ToneDone, "cancelled": ToneDone, "blocked": ToneStuck,
		"in_review": ToneWaiting, "in_progress": ToneWorking, "todo": ToneWorking,
	} {
		if got := ForStatus(status); got != want {
			t.Errorf("ForStatus(%q) = %q, want %q", status, got, want)
		}
	}
}

// An unexplained stall must read red, the same as an explicit block.
func TestForParking(t *testing.T) {
	for category, want := range map[string]string{
		parking.CategoryDone:                 ToneDone,
		parking.CategoryAwaitReview:          ToneWaiting,
		parking.CategoryWaitingPerson:        ToneWaiting,
		parking.CategoryBlocked:              ToneStuck,
		parking.CategoryStalledDelivery:      ToneStuck,
		parking.CategoryStalledUnclosed:      ToneStuck,
		parking.CategoryStalledReplyUnclosed: ToneStuck,
		parking.CategoryRunning:              ToneWorking,
	} {
		if got := ForParking(category); got != want {
			t.Errorf("ForParking(%q) = %q, want %q", category, got, want)
		}
	}
}

func TestClip(t *testing.T) {
	if got := Clip("  a \n b  "); got != "a b" {
		t.Errorf("Clip collapsed to %q", got)
	}
	if got := Clip(strings.Repeat("字", MaxLen+5)); len([]rune(got)) != MaxLen {
		t.Errorf("Clip length = %d", len([]rune(got)))
	}
}
