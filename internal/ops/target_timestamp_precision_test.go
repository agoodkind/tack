package ops

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// TestFDBRestoreCommandRejectsSubsecondTarget checks refusal of fractional
// seconds and an error that includes the required whole-second format.
func TestFDBRestoreCommandRejectsSubsecondTarget(t *testing.T) {
	target := time.Date(2026, 8, 30, 1, 5, 0, 900000000, time.UTC)
	got, err := fdbRestoreCommand(drillDestURL, &target)
	if err == nil {
		t.Fatalf("restore command accepted a subsecond target: %q", got)
	}

	if got != nil {
		t.Fatalf("refused target returned a command: %q", got)
	}

	for _, want := range []string{
		"2026-08-30T01:05:00.9Z",
		fdbTimestampArgForm,
		"2026/08/30.01:05:00+0000",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal must include %q: %v", want, err)
		}
	}
}

// TestParseFDBTargetTime checks that supported formats and offsets identify
// the same instant.
func TestParseFDBTargetTime(t *testing.T) {
	want := time.Date(2026, 8, 30, 1, 5, 0, 0, time.UTC)

	tests := []struct {
		name  string
		input string
	}{
		{name: "parser accepts RFC3339 UTC", input: "2026-08-30T01:05:00Z"},
		{name: "parser accepts an RFC3339 offset", input: "2026-08-29T21:05:00-04:00"},
		{name: "parser accepts FoundationDB format", input: "2026/08/30.01:05:00+0000"},
	}
	for _, test := range tests {
		t.Run(
			test.name,
			func(t *testing.T) {
				got, err := parseFDBTargetTime(test.input)
				if err != nil {
					t.Fatalf("parseFDBTargetTime(%q): %v", test.input, err)
				}

				if !got.Equal(want) {
					t.Fatalf(
						"parseFDBTargetTime(%q) = %s, want %s",
						test.input,
						formatFDBTime(got),
						formatFDBTime(want),
					)
				}
			},
		)
	}
}

// TestParseFDBTargetTimeRejectsAmbiguousInput checks refusal of missing zones,
// invalid formats, and empty input rather than guessing a restore time.
func TestParseFDBTargetTimeRejectsAmbiguousInput(t *testing.T) {
	for _, input := range []string{"yesterday", "2026-08-30", "2026-08-30T01:05:00", ""} {
		if _, err := parseFDBTargetTime(input); err == nil {
			t.Fatalf("parseFDBTargetTime(%q) accepted ambiguous input", input)
		}
	}
}

// TestParseFDBTargetTimeRejectsSubsecondInput checks refusal of fractions that
// FoundationDB's --timestamp cannot represent. time.Parse accepts these
// fractions even when its layout does not include them.
func TestParseFDBTargetTimeRejectsSubsecondInput(t *testing.T) {
	for _, input := range []string{
		"2026-08-30T01:05:00.9Z",
		"2026-08-29T21:05:00.000000001-04:00",
		"2026/08/30.01:05:00.9+0000",
	} {
		t.Run(
			input,
			func(t *testing.T) {
				got, err := parseFDBTargetTime(input)
				if err == nil {
					t.Fatalf(
						"parseFDBTargetTime(%q) = %s, want refusal of fractional seconds",
						input,
						formatFDBTime(got),
					)
				}

				if !strings.Contains(err.Error(), fdbTimestampArgForm) {
					t.Fatalf("refusal must specify the required timestamp format: %v", err)
				}
			},
		)
	}
}

// TestParseFDBTargetTimeUsesExplicitZeroTarget distinguishes an explicit zero
// time from an absent target. Only an absent target selects the latest data.
func TestParseFDBTargetTimeUsesExplicitZeroTarget(t *testing.T) {
	parsed, err := parseFDBTargetTime("0001-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("parseFDBTargetTime with an explicit zero time: %v", err)
	}

	latest, err := fdbRestoreCommand(drillDestURL, nil)
	if err != nil {
		t.Fatalf("fdbRestoreCommand without a target: %v", err)
	}

	explicit, err := fdbRestoreCommand(drillDestURL, &parsed)
	if err != nil {
		t.Fatalf("fdbRestoreCommand with an explicit zero time: %v", err)
	}

	if slices.Equal(explicit, latest) {
		t.Fatalf("explicit zero target selected the latest-data restore: %q", explicit)
	}

	if !slices.Contains(explicit, "0001/01/01.00:00:00+0000") {
		t.Fatalf("restore command does not include the explicit zero target: %q", explicit)
	}
}
