package ops

import (
	"strings"
	"testing"
)

// ybTabletsPage is a tablet server tablets page in the shape yugabyte serves
// it, trimmed from a QA ledger node read on 2026-09-15: keyed by tablet id,
// with the state and file count the wait reads beside fields it ignores.
const ybTabletsPage = `{
"0084c4a62d894302b2934a2de6161384":{"namespace":"tack","table_name":"events_2026_09_07_org_id_shard_seq_idx","table_id":"000040000000300080000000000040ac","partition":"hash_split: [0xAAAA, 0xFFFF]","state":"RUNNING","hidden":false,"num_sst_files":4,"on_disk_size":{"total_size":"633.4K"}},
"1a2b3c4d5e6f47089a0b1c2d3e4f5061":{"namespace":"tack","table_name":"users","table_id":"000040000000300080000000000040b0","partition":"hash_split: [0x0000, 0xFFFF]","state":"RUNNING","hidden":false,"num_sst_files":2,"on_disk_size":{"total_size":"12.0K"}},
"2b3c4d5e6f7a48099b0c1d2e3f405162":{"namespace":"tack","table_name":"api_tokens","table_id":"000040000000300080000000000040b4","partition":"hash_split: [0x0000, 0xFFFF]","state":"BOOTSTRAPPING","hidden":false,"num_sst_files":0,"on_disk_size":{"total_size":"0B"}}
}`

// TestYBScratchTabletCountersReadsTheServedPage proves the counters come from
// the page yugabyte actually serves: running tablets are counted by state, and
// files are summed across every tablet, including one still bootstrapping.
func TestYBScratchTabletCountersReadsTheServedPage(t *testing.T) {
	counters, err := ybScratchTabletCounters(t.Context(), ybTabletsPage)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if counters[ybCounterRunningTablets] != 2 {
		t.Errorf("running_tablets = %d, want 2", counters[ybCounterRunningTablets])
	}
	if counters[ybCounterSSTFiles] != 6 {
		t.Errorf("sst_files = %d, want 6", counters[ybCounterSSTFiles])
	}
}

// TestParseYBScratchDataBytesReadsDuOutput proves the byte count is read from
// the shape `du -sb` prints, and that a failed or empty du is an error rather
// than a zero that would read as a reading.
func TestParseYBScratchDataBytesReadsDuOutput(t *testing.T) {
	bytes, err := parseYBScratchDataBytes(t.Context(), 0, "48123904\t/home/yugabyte/var/data\n")
	if err != nil || bytes != 48123904 {
		t.Fatalf("bytes = %d, err = %v, want 48123904", bytes, err)
	}
	if _, err := parseYBScratchDataBytes(t.Context(), 1, "du: cannot access '/home/yugabyte/var/data'"); err == nil {
		t.Fatal("a du that exited non-zero must be an error")
	}
	if _, err := parseYBScratchDataBytes(t.Context(), 0, ""); err == nil {
		t.Fatal("an empty du output must be an error")
	}
	bytes, err = parseYBScratchDataBytes(t.Context(), 1, "51200000\t/home/yugabyte/var/data\n")
	if err != nil || bytes != 51200000 {
		t.Fatalf("bytes = %d, err = %v, want a total from a du that saw a file vanish", bytes, err)
	}
	if _, err := parseYBScratchDataBytes(t.Context(), 2, "51200000\t/home/yugabyte/var/data\n"); err == nil {
		t.Fatal("a du that exited 2 must be an error")
	}
}

// TestParseYBScratchRestartsReadsGrepCount proves the restart count is read
// the way grep -c reports it: exit 1 with a zero count is no restart, and a
// log grep could not read is an error, never a zero.
func TestParseYBScratchRestartsReadsGrepCount(t *testing.T) {
	for _, tc := range []struct {
		exitCode int
		stdout   string
		want     int64
	}{{exitCode: 1, stdout: "0\n", want: 0}, {exitCode: 0, stdout: "2\n", want: 2}} {
		got, err := parseYBScratchRestarts(t.Context(), tc.exitCode, tc.stdout)
		if err != nil || got != tc.want {
			t.Errorf("exit %d %q: restarts = %d, err = %v, want %d", tc.exitCode, tc.stdout, got, err, tc.want)
		}
	}
	if _, err := parseYBScratchRestarts(t.Context(), 2, "grep: yugabyted.log: No such file or directory"); err == nil {
		t.Fatal("a log grep could not read must be an error")
	}
}

// TestYBRestorationFailureNamesATerminalState proves a restoration the master
// failed or cancelled ends the wait, and one still restoring or restored does
// not.
func TestYBRestorationFailureNamesATerminalState(t *testing.T) {
	failed := `{"restorations": [{"id": "a1", "snapshot_id": "b2", "state": "FAILED"}]}`
	if got := ybRestorationFailure(failed); !strings.Contains(got, "FAILED") {
		t.Errorf("failure = %q, want FAILED named", got)
	}
	for _, listing := range []string{
		`{"restorations": []}`,
		`{"restorations": [{"id": "a1", "snapshot_id": "b2", "state": "RESTORING"}]}`,
		`{"restorations": [{"id": "a1", "snapshot_id": "b2", "state": "RESTORED"}]}`,
	} {
		if got := ybRestorationFailure(listing); got != "" {
			t.Errorf("listing %s: failure = %q, want none", listing, got)
		}
	}
}

// TestYBScratchTabletCountersRefusesAnotherPage proves a page that is not the
// tablets page yields an error, so an endpoint that answers with something
// else never vouches for progress.
func TestYBScratchTabletCountersRefusesAnotherPage(t *testing.T) {
	if _, err := ybScratchTabletCounters(t.Context(), "<html>Error retrieving leader master URL</html>"); err == nil {
		t.Fatal("an HTML page must not parse as tablet counters")
	}
}
