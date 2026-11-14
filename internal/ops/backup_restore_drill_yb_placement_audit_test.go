package ops

import (
	"strings"
	"testing"
)

// TestParseYBPlacementAuditRejectsOutputItCannotRead checks that malformed
// audit output fails instead of indicating a complete tablet restoration.
func TestParseYBPlacementAuditRejectsOutputItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
	}{
		{name: "empty", out: ""},
		{name: "counts only", out: "expected 4\nplaced 4\n"},
		{name: "no missing marker", out: "expected 4\nplaced 4\ntablets\n"},
		{name: "unlabelled count", out: "4\nplaced 4\nmissing\n"},
		{name: "count is not a number", out: "expected many\nplaced 4\nmissing\n"},
		{name: "negative count", out: "expected -1\nplaced 4\nmissing\n"},
		{name: "counts disagree with the list", out: "expected 4\nplaced 1\nmissing\ntable:new\n"},
		{name: "placed exceeds expected", out: "expected 1\nplaced 4\nmissing\n"},
	} {
		t.Run(
			tc.name,
			func(t *testing.T) {
				if _, err := parseYBPlacementAudit(tc.out); err == nil {
					t.Fatalf("parsing %q must fail rather than report complete placement", tc.out)
				}
			},
		)
	}
}

// TestParseYBPlacementAuditReadsAWholeRun checks counts and identities,
// including whitespace padding from wc.
func TestParseYBPlacementAuditReadsAWholeRun(t *testing.T) {
	audit, err := parseYBPlacementAudit(
		"expected      5\nplaced      3\nmissing\ntableA:newA\ntableB:newB\n",
	)
	if err != nil {
		t.Fatalf("parseYBPlacementAudit: %v", err)
	}

	if audit.Expected != 5 || audit.Placed != 3 {
		t.Fatalf("audit = expected %d, placed %d; want 5 and 3", audit.Expected, audit.Placed)
	}

	if len(audit.Missing) != 2 || audit.Missing[0] != "tableA:newA" {
		t.Fatalf("missing = %v, want the two named tablets", audit.Missing)
	}
}

// TestYBPlacementVerdictRejectsUnattemptedTablets checks that omitted
// script chunks cannot reduce the expected tablet count and pass the audit.
func TestYBPlacementVerdictRejectsUnattemptedTablets(t *testing.T) {
	audit := ybPlacementAudit{Expected: 40, Placed: 40, Missing: nil}

	err := ybPlacementVerdict(audit, 130)
	if err == nil {
		t.Fatal("a placement that attempted 40 of 130 tablets must fail the drill")
	}

	if !strings.Contains(err.Error(), "attempted 40 of the 130 tablets") {
		t.Fatalf("the failure must report both counts, got: %v", err)
	}
}

// TestYBPlacementVerdictReportsOmittedIdentityCount checks exact missing counts and
// the omitted identity count when the error sample limit applies.
func TestYBPlacementVerdictReportsOmittedIdentityCount(t *testing.T) {
	missing := make([]string, 0, 500)

	for i := range 500 {
		missing = append(missing, string(rune('a'+i%26))+"tablet")
	}

	audit := ybPlacementAudit{Expected: 600, Placed: 100, Missing: missing}

	err := ybPlacementVerdict(audit, 600)
	if err == nil {
		t.Fatal("500 missing tablets must fail the drill")
	}

	if !strings.Contains(err.Error(), "500 of the 600 attempted tablets did not record a copy") {
		t.Fatalf("the failure must report the exact number missing, got: %v", err)
	}

	if !strings.Contains(err.Error(), "and 480 more") {
		t.Fatalf("the failure must report the omitted identity count, got: %v", err)
	}
}

// TestYBPlacementVerdictPassesACompleteRun checks successful accounting.
func TestYBPlacementVerdictPassesACompleteRun(t *testing.T) {
	audit := ybPlacementAudit{Expected: 130, Placed: 130, Missing: nil}
	if err := ybPlacementVerdict(audit, 130); err != nil {
		t.Fatalf("a complete placement must pass: %v", err)
	}
}

// TestCountDistinctTabletsCountsTabletsNotRows checks deduplication using the
// placement records' table-and-tablet identity.
func TestCountDistinctTabletsCountsTabletsNotRows(t *testing.T) {
	remaps := []ybTabletRemap{
		{table: "t1", old: "o1", new: "n1"},
		{table: "t1", old: "o1", new: "n1"},
		{table: "t2", old: "o2", new: "n2"},
	}
	if got := countDistinctTablets(remaps); got != 2 {
		t.Fatalf("countDistinctTablets = %d, want 2", got)
	}
}
