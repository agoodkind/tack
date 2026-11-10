package ops

import (
	"strings"
	"testing"
)

// TestParseYBPlacementAuditRejectsOutputItCannotRead checks that every listed
// unreadable output shape fails the drill. The placement audit must detect
// missing tablet copies before a restore can pass; unreadable output cannot
// establish completeness.
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
					t.Fatalf("parsing %q must fail rather than report nothing missing", tc.out)
				}
			},
		)
	}
}

// TestParseYBPlacementAuditReadsAWholeRun checks that the parser preserves
// shell-reported counts and identities, including counts padded by wc.
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

// TestYBPlacementVerdictCatchesTabletsThatWereNeverTried checks that skipped
// tablets fail the verdict. A script that never ran would appear complete for a
// smaller corpus if the audit counted only completed copies.
func TestYBPlacementVerdictCatchesTabletsThatWereNeverTried(t *testing.T) {
	audit := ybPlacementAudit{Expected: 40, Placed: 40, Missing: nil}

	err := ybPlacementVerdict(audit, 130)
	if err == nil {
		t.Fatal("a placement that attempted 40 of 130 tablets must fail the drill")
	}

	if !strings.Contains(err.Error(), "attempted 40 of the 130 tablets") {
		t.Fatalf("the failure must name both counts, got: %v", err)
	}
}

// TestYBPlacementVerdictNamesHowManyItLeftOut checks that a failure involving
// the whole corpus reports the exact missing count and the number of omitted
// identities.
func TestYBPlacementVerdictNamesHowManyItLeftOut(t *testing.T) {
	missing := make([]string, 0, 500)

	for i := range 500 {
		missing = append(missing, string(rune('a'+i%26))+"tablet")
	}

	audit := ybPlacementAudit{Expected: 600, Placed: 100, Missing: missing}

	err := ybPlacementVerdict(audit, 600)
	if err == nil {
		t.Fatal("500 missing tablets must fail the drill")
	}

	if !strings.Contains(err.Error(), "500 of the 600 tablets it attempted recorded no copy") {
		t.Fatalf("the failure must name the exact number missing, got: %v", err)
	}

	if !strings.Contains(err.Error(), "and 480 more") {
		t.Fatalf("the failure must say how many names it left out, got: %v", err)
	}
}

// TestYBPlacementVerdictPassesACompleteRun checks that accounting accepts a
// complete restore.
func TestYBPlacementVerdictPassesACompleteRun(t *testing.T) {
	audit := ybPlacementAudit{Expected: 130, Placed: 130, Missing: nil}
	if err := ybPlacementVerdict(audit, 130); err != nil {
		t.Fatalf("a complete placement must pass: %v", err)
	}
}

// TestCountDistinctTabletsCountsTabletsNotRows checks that duplicate mapping
// rows count once per table and tablet, matching ledger identity.
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
