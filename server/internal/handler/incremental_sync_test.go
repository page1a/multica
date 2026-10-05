package handler

import (
	"testing"
	"time"
)

func TestIncrementalCursorRoundTrip(t *testing.T) {
	want := incrementalCursor{At: time.Date(2026, 10, 4, 1, 2, 3, 456000000, time.UTC), ID: "11111111-1111-1111-1111-111111111111"}
	got, err := decodeIncrementalCursor(encodeIncrementalCursor(want))
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if !got.At.Equal(want.At) || got.ID != want.ID {
		t.Fatalf("cursor = %#v, want %#v", got, want)
	}
}

func TestIncrementalCursorRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{"", "not-base64", encodeIncrementalCursor(incrementalCursor{ID: "x"})} {
		if _, err := decodeIncrementalCursor(value); err == nil {
			t.Fatalf("decode %q unexpectedly succeeded", value)
		}
	}
}
