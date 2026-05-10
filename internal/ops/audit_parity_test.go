package ops

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeParityScanner struct {
	counts          *parityCounts
	countsErr       error
	examples        []parityExample
	examplesErr     error
	scanCountsCalls int
	scanExampleArgs []int
}

func (f *fakeParityScanner) ScanCounts(_ context.Context, _ time.Time, _ time.Time) (*parityCounts, error) {
	f.scanCountsCalls++
	if f.countsErr != nil {
		return nil, f.countsErr
	}
	c := *f.counts
	return &c, nil
}

func (f *fakeParityScanner) ScanExamples(_ context.Context, _ time.Time, _ time.Time, limit int) ([]parityExample, error) {
	f.scanExampleArgs = append(f.scanExampleArgs, limit)
	if f.examplesErr != nil {
		return nil, f.examplesErr
	}
	out := make([]parityExample, len(f.examples))
	copy(out, f.examples)
	return out, nil
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return tm
}

func parityWindow(t *testing.T) (time.Time, time.Time) {
	return mustParseTime(t, "2026-05-09T00:00:00Z"), mustParseTime(t, "2026-05-09T01:00:00Z")
}

func TestRunParityScan_zeroRows(t *testing.T) {
	scanner := &fakeParityScanner{counts: &parityCounts{}}
	from, to := parityWindow(t)
	result, err := runParityScan(context.Background(), scanner, from, to, 1.0)
	if err != nil {
		t.Fatalf("runParityScan: %v", err)
	}
	if result.Total != 0 {
		t.Fatalf("Total = %d, want 0", result.Total)
	}
	if result.MatchedFraction != 1.0 {
		t.Fatalf("MatchedFraction = %v, want 1.0 for empty window", result.MatchedFraction)
	}
	if len(result.Examples) != 0 {
		t.Fatalf("Examples = %d, want 0", len(result.Examples))
	}
	if len(scanner.scanExampleArgs) != 0 {
		t.Fatalf("ScanExamples should not be called when ContentDiff=0, got %d calls", len(scanner.scanExampleArgs))
	}
}

func TestRunParityScan_fullMatch(t *testing.T) {
	scanner := &fakeParityScanner{counts: &parityCounts{Matched: 42}}
	from, to := parityWindow(t)
	result, err := runParityScan(context.Background(), scanner, from, to, 1.0)
	if err != nil {
		t.Fatalf("runParityScan: %v", err)
	}
	if result.Total != 42 {
		t.Fatalf("Total = %d, want 42", result.Total)
	}
	if result.MatchedFraction != 1.0 {
		t.Fatalf("MatchedFraction = %v, want 1.0", result.MatchedFraction)
	}
	if len(scanner.scanExampleArgs) != 0 {
		t.Fatalf("ScanExamples should not be called without content_diff")
	}
}

func TestRunParityScan_onlyLegacyDrift(t *testing.T) {
	scanner := &fakeParityScanner{counts: &parityCounts{Matched: 90, OnlyLegacy: 10}}
	from, to := parityWindow(t)
	result, err := runParityScan(context.Background(), scanner, from, to, 0.95)
	if err != nil {
		t.Fatalf("runParityScan: %v", err)
	}
	if result.Total != 100 {
		t.Fatalf("Total = %d, want 100", result.Total)
	}
	if result.MatchedFraction != 0.9 {
		t.Fatalf("MatchedFraction = %v, want 0.9", result.MatchedFraction)
	}
	if result.Counts.OnlyLegacy != 10 {
		t.Fatalf("OnlyLegacy = %d, want 10", result.Counts.OnlyLegacy)
	}
}

func TestRunParityScan_onlyV2Drift(t *testing.T) {
	scanner := &fakeParityScanner{counts: &parityCounts{Matched: 5, OnlyV2: 5}}
	from, to := parityWindow(t)
	result, err := runParityScan(context.Background(), scanner, from, to, 1.0)
	if err != nil {
		t.Fatalf("runParityScan: %v", err)
	}
	if result.Counts.OnlyV2 != 5 {
		t.Fatalf("OnlyV2 = %d, want 5", result.Counts.OnlyV2)
	}
	if result.MatchedFraction != 0.5 {
		t.Fatalf("MatchedFraction = %v, want 0.5", result.MatchedFraction)
	}
}

func TestRunParityScan_contentDiffPullsExamples(t *testing.T) {
	exampleID := uuid.New()
	scanner := &fakeParityScanner{
		counts: &parityCounts{Matched: 7, ContentDiff: 3},
		examples: []parityExample{{
			EventID: exampleID,
			Diffs: []parityFieldDiff{
				{Field: "action", Legacy: "create", V2: "update"},
			},
		}},
	}
	from, to := parityWindow(t)
	result, err := runParityScan(context.Background(), scanner, from, to, 0.5)
	if err != nil {
		t.Fatalf("runParityScan: %v", err)
	}
	if len(scanner.scanExampleArgs) != 1 {
		t.Fatalf("ScanExamples called %d times, want 1", len(scanner.scanExampleArgs))
	}
	if scanner.scanExampleArgs[0] != parityExampleLimit {
		t.Fatalf("ScanExamples limit = %d, want %d", scanner.scanExampleArgs[0], parityExampleLimit)
	}
	if len(result.Examples) != 1 {
		t.Fatalf("Examples = %d, want 1", len(result.Examples))
	}
	if result.Examples[0].EventID != exampleID {
		t.Fatalf("Examples[0].EventID = %v, want %v", result.Examples[0].EventID, exampleID)
	}
	if result.MatchedFraction != 0.7 {
		t.Fatalf("MatchedFraction = %v, want 0.7", result.MatchedFraction)
	}
}

func TestRunParityScan_countsErrorPropagates(t *testing.T) {
	scanner := &fakeParityScanner{countsErr: errors.New("boom")}
	from, to := parityWindow(t)
	if _, err := runParityScan(context.Background(), scanner, from, to, 1.0); err == nil {
		t.Fatalf("expected error from ScanCounts")
	}
}

func TestRunParityScan_examplesErrorPropagates(t *testing.T) {
	scanner := &fakeParityScanner{
		counts:      &parityCounts{Matched: 1, ContentDiff: 1},
		examplesErr: errors.New("rows"),
	}
	from, to := parityWindow(t)
	if _, err := runParityScan(context.Background(), scanner, from, to, 1.0); err == nil {
		t.Fatalf("expected error from ScanExamples")
	}
}
