package ops

import (
	"strings"
	"testing"
)

// TestParseYBPlacementAuditRejectsOutputItCannotRead proves an audit reading
// the drill cannot make sense of fails rather than reading as nothing missing.
// The audit is the only thing between a restore missing tablet files and a
// passing drill, so every unreadable shape has to be an error.
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
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseYBPlacementAudit(tc.out); err == nil {
				t.Fatalf("parsing %q must fail rather than report nothing missing", tc.out)
			}
		})
	}
}

// TestParseYBPlacementAuditReadsAWholeRun proves the counts and the names come
// back as the shell reported them, padding from wc included.
func TestParseYBPlacementAuditReadsAWholeRun(t *testing.T) {
	audit, err := parseYBPlacementAudit("expected      5\nplaced      3\nmissing\ntableA:newA\ntableB:newB\n")
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

// TestYBPlacementVerdictCatchesTabletsThatWereNeverTried proves a placement
// that skipped tablets outright fails, which is what catches a chunk that
// never ran. Counting only what was found would read that as a smaller corpus
// that placed everything.
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

// TestYBPlacementVerdictNamesHowManyItLeftOut proves a corpus-sized failure
// still reports an exact count and says how many names it did not list, so
// nothing is dropped without saying so.
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
	if !strings.Contains(err.Error(), "so 500 have no files") {
		t.Fatalf("the failure must name the exact number missing, got: %v", err)
	}
	if !strings.Contains(err.Error(), "and 480 more") {
		t.Fatalf("the failure must say how many names it left out, got: %v", err)
	}
}

// TestYBPlacementVerdictPassesACompleteRun locks that a whole restore is not
// failed by the accounting.
func TestYBPlacementVerdictPassesACompleteRun(t *testing.T) {
	audit := ybPlacementAudit{Expected: 130, Placed: 130, Missing: nil}
	if err := ybPlacementVerdict(audit, 130); err != nil {
		t.Fatalf("a complete placement must pass: %v", err)
	}
}

// TestCountDistinctTabletsCountsTabletsNotRows proves the number the audit is
// compared against counts each tablet of each table once, which is the same
// identity the ledgers record.
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
