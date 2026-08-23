package audit

import (
	"testing"
	"time"
)

func TestQueryCursorRoundTrip(t *testing.T) {
	eventTime := time.Date(2026, 8, 23, 12, 30, 45, 123456000, time.UTC)
	cursor := MarshalQueryCursor(eventTime, 42)
	gotTime, gotSeq, err := UnmarshalQueryCursor(t.Context(), cursor)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !gotTime.Equal(eventTime) || gotSeq != 42 {
		t.Fatalf("round trip = (%s, %d), want (%s, 42)", gotTime, gotSeq, eventTime)
	}
}

func TestQueryCursorRejectsGarbage(t *testing.T) {
	for _, cursor := range []string{"not base64 ///", "bm8tc2VwYXJhdG9y", ""} {
		if _, _, err := UnmarshalQueryCursor(t.Context(), cursor); err == nil {
			t.Fatalf("UnmarshalQueryCursor(%q) accepted garbage", cursor)
		}
	}
}
