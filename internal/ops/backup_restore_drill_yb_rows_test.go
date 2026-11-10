package ops

import "testing"

// TestParseYBDrillRowCountRejectsAnythingThatIsNotACount checks rejection
// of unreadable counts. Previously, an exec that exited successfully without
// printing output produced an empty string that passed comparison against "0".
// The drill could accept a restored table without establishing its row count.
func TestParseYBDrillRowCountRejectsAnythingThatIsNotACount(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
	}{
		{name: "printed nothing", out: ""},
		{name: "printed only whitespace", out: " \n\t "},
		{name: "printed an error", out: "ERROR:  permission denied for table users"},
		{name: "printed a psql table header", out: " count \n-------\n     3\n(1 row)"},
		{name: "printed a negative number", out: "-1"},
		{name: "printed a float", out: "3.0"},
	} {
		t.Run(
			tc.name,
			func(t *testing.T) {
				if _, err := parseYBDrillRowCount(tc.out); err == nil {
					t.Fatalf(
						"parsing %q as a row count must fail rather than pass the drill",
						tc.out,
					)
				}
			},
		)
	}
}

// TestParseYBDrillRowCountReadsRealCounts checks valid counts, including zero.
// The row assertion must still reject an empty table after successfully reading
// its zero count.
func TestParseYBDrillRowCountReadsRealCounts(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want int64
	}{
		{out: "0\n", want: 0},
		{out: "42\n", want: 42},
		{out: "  1275  ", want: 1275},
		{out: "9007199254740993", want: 9007199254740993},
	} {
		t.Run(
			tc.out,
			func(t *testing.T) {
				got, err := parseYBDrillRowCount(tc.out)
				if err != nil {
					t.Fatalf("parseYBDrillRowCount(%q): %v", tc.out, err)
				}

				if got != tc.want {
					t.Fatalf("parseYBDrillRowCount(%q) = %d, want %d", tc.out, got, tc.want)
				}
			},
		)
	}
}
