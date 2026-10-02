// Package progress owns the vocabulary of the goal + progress subtitle
// (DENE-1037): which sources may write a progress line and which tone (dot
// colour) a line carries. The handler and the task service both write
// progress, so the rules live here once.
package progress

import (
	"strings"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/parking"
)

// Sources, in display priority for issues: an agent's own line, then the
// close summary, then the parking (stall patrol) summary. Agent and close
// lines are both explicit statements, so between them the latest wins; the
// parking summary only fills in while neither exists. Chats use agent, then
// a model recap of the latest turn ("model"), or the reply itself when the
// deployment has no model configured ("reply").
const (
	SourceAgent = "agent"
	SourceClose = "close"
	SourceModel = "model"
	SourceReply = "reply"
)

// Tones the dot colour reads: blue / yellow / red / green.
const (
	ToneWorking = "working"
	ToneWaiting = "waiting"
	ToneStuck   = "stuck"
	ToneDone    = "done"
)

// MaxLen bounds one progress line in runes.
const MaxLen = 500

// NormalizeTone accepts a tone a caller passed explicitly. The empty string
// is valid and means "derive it".
func NormalizeTone(tone string) (string, bool) {
	tone = strings.ToLower(strings.TrimSpace(tone))
	switch tone {
	case "", ToneWorking, ToneWaiting, ToneStuck, ToneDone:
		return tone, true
	}
	return "", false
}

// ForStatus derives a tone from an issue's effective (built-in) status.
func ForStatus(status string) string {
	switch status {
	case issuestatus.Done, issuestatus.Cancelled:
		return ToneDone
	case issuestatus.Blocked:
		return ToneStuck
	case issuestatus.InReview:
		return ToneWaiting
	default:
		return ToneWorking
	}
}

// ForParking maps a parking category onto a tone: a stall the agent did not
// explain is stuck, the same as an explicit block.
func ForParking(category string) string {
	switch category {
	case parking.CategoryDone:
		return ToneDone
	case parking.CategoryAwaitReview, parking.CategoryWaitingPerson:
		return ToneWaiting
	case parking.CategoryBlocked:
		return ToneStuck
	}
	if parking.Unexplained(category) {
		return ToneStuck
	}
	return ToneWorking
}

// Clip trims and bounds a line. Returns "" when nothing is left.
func Clip(text string) string {
	text = strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	if r := []rune(text); len(r) > MaxLen {
		text = strings.TrimSpace(string(r[:MaxLen]))
	}
	return text
}
