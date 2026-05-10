package ops

import (
	"os"
	"testing"

	"github.com/google/uuid"
)

func TestCollectFieldDiffs_allFields(t *testing.T) {
	la := uuid.New()
	va := uuid.New()
	le := uuid.New()
	ve := uuid.New()
	diffs := collectFieldDiffs(
		la, va,
		"create", "update",
		"issue", "epic",
		le, ve,
		`{"k":1}`, `{"k":2}`,
	)
	if len(diffs) != 5 {
		t.Fatalf("expected 5 diffs, got %d: %+v", len(diffs), diffs)
	}
	wantFields := map[string]struct{}{
		"actor_id":    {},
		"action":      {},
		"entity_kind": {},
		"entity_id":   {},
		"context":     {},
	}
	for _, d := range diffs {
		if _, ok := wantFields[d.Field]; !ok {
			t.Fatalf("unexpected diff field %q", d.Field)
		}
	}
}

func TestCollectFieldDiffs_noDiff(t *testing.T) {
	a := uuid.New()
	e := uuid.New()
	diffs := collectFieldDiffs(a, a, "x", "x", "issue", "issue", e, e, "ctx", "ctx")
	if len(diffs) != 0 {
		t.Fatalf("expected 0 diffs, got %d", len(diffs))
	}
}

func TestReadParityWindow_threshold(t *testing.T) {
	t.Setenv(parityFromEnv, "2026-05-09T00:00:00Z")
	t.Setenv(parityToEnv, "2026-05-09T01:00:00Z")
	t.Setenv(parityThresholdEnv, "0.95")
	from, to, threshold, err := readParityWindow()
	if err != nil {
		t.Fatalf("readParityWindow: %v", err)
	}
	if !from.Equal(mustParseTime(t, "2026-05-09T00:00:00Z")) {
		t.Fatalf("from = %v", from)
	}
	if !to.Equal(mustParseTime(t, "2026-05-09T01:00:00Z")) {
		t.Fatalf("to = %v", to)
	}
	if threshold != 0.95 {
		t.Fatalf("threshold = %v, want 0.95", threshold)
	}
}

func TestReadParityWindow_defaultThreshold(t *testing.T) {
	t.Setenv(parityFromEnv, "2026-05-09T00:00:00Z")
	t.Setenv(parityToEnv, "2026-05-09T01:00:00Z")
	prev, hadPrev := os.LookupEnv(parityThresholdEnv)
	if err := os.Unsetenv(parityThresholdEnv); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv(parityThresholdEnv, prev)
			return
		}
		_ = os.Unsetenv(parityThresholdEnv)
	})
	_, _, threshold, err := readParityWindow()
	if err != nil {
		t.Fatalf("readParityWindow: %v", err)
	}
	if threshold != 1.0 {
		t.Fatalf("threshold = %v, want 1.0", threshold)
	}
}

func TestReadParityWindow_rejectsInvertedWindow(t *testing.T) {
	t.Setenv(parityFromEnv, "2026-05-09T01:00:00Z")
	t.Setenv(parityToEnv, "2026-05-09T00:00:00Z")
	if _, _, _, err := readParityWindow(); err == nil {
		t.Fatalf("expected error for inverted window")
	}
}

func TestReadParityWindow_rejectsBadThreshold(t *testing.T) {
	t.Setenv(parityFromEnv, "2026-05-09T00:00:00Z")
	t.Setenv(parityToEnv, "2026-05-09T01:00:00Z")
	t.Setenv(parityThresholdEnv, "1.5")
	if _, _, _, err := readParityWindow(); err == nil {
		t.Fatalf("expected error for threshold > 1")
	}
}
