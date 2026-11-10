package ops

import (
	"regexp"
	"strings"
	"testing"
)

// ybTabletsPage contains a tablet server status page from a QA ledger node
// on 2026-09-15, with unused content removed. yugabyte indexes entries by
// tablet ID. The fixture includes the state and file count that the wait reads
// alongside fields that it ignores.
const ybTabletsPage = `{
"0084c4a62d894302b2934a2de6161384":{"namespace":"tack","table_name":"events_2026_09_07_org_id_shard_seq_idx","table_id":"000040000000300080000000000040ac","partition":"hash_split: [0xAAAA, 0xFFFF]","state":"RUNNING","hidden":false,"num_sst_files":4,"on_disk_size":{"total_size":"633.4K"}},
"1a2b3c4d5e6f47089a0b1c2d3e4f5061":{"namespace":"tack","table_name":"users","table_id":"000040000000300080000000000040b0","partition":"hash_split: [0x0000, 0xFFFF]","state":"RUNNING","hidden":false,"num_sst_files":2,"on_disk_size":{"total_size":"12.0K"}},
"2b3c4d5e6f7a48099b0c1d2e3f405162":{"namespace":"tack","table_name":"api_tokens","table_id":"000040000000300080000000000040b4","partition":"hash_split: [0x0000, 0xFFFF]","state":"BOOTSTRAPPING","hidden":false,"num_sst_files":0,"on_disk_size":{"total_size":"0B"}}
}`

// TestYBScratchTabletCountersReadsTheServedPage checks counters against the
// served yugabyte page shape. The parser counts running tablets by state and
// sums files across all tablets, including a bootstrapping tablet.
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

// TestParseYBScratchDataBytesReadsDuOutput checks the byte count from du -sb
// output. A failed command without a numeric total or an empty result must
// return an error rather than a zero reading.
func TestParseYBScratchDataBytesReadsDuOutput(t *testing.T) {
	bytes, err := parseYBScratchDataBytes(t.Context(), 0, "48123904\t/home/yugabyte/var/data\n")
	if err != nil || bytes != 48123904 {
		t.Fatalf("bytes = %d, err = %v, want 48123904", bytes, err)
	}

	if _, err := parseYBScratchDataBytes(
		t.Context(),
		1,
		"du: cannot access '/home/yugabyte/var/data'",
	); err == nil {
		t.Fatal("a du that exited non-zero must be an error")
	}

	if _, err := parseYBScratchDataBytes(t.Context(), 0, ""); err == nil {
		t.Fatal("an empty du output must be an error")
	}

	bytes, err = parseYBScratchDataBytes(t.Context(), 1, "51200000\t/home/yugabyte/var/data\n")
	if err != nil || bytes != 51200000 {
		t.Fatalf("bytes = %d, err = %v, want a total from a du that saw a file vanish", bytes, err)
	}

	if _, err := parseYBScratchDataBytes(
		t.Context(),
		2,
		"51200000\t/home/yugabyte/var/data\n",
	); err == nil {
		t.Fatal("a du that exited 2 must be an error")
	}
}

// TestParseYBScratchRestartsReadsGrepCount checks grep -c results. Exit 1
// with a zero count means that a restart did not occur. An unreadable log
// must return an error rather than zero.
func TestParseYBScratchRestartsReadsGrepCount(t *testing.T) {
	for _, tc := range []struct {
		exitCode int
		stdout   string
		want     int64
	}{
		{exitCode: 1, stdout: "0\n", want: 0},
		{exitCode: 0, stdout: "2\n", want: 2},
	} {
		got, err := parseYBScratchRestarts(t.Context(), tc.exitCode, tc.stdout)
		if err != nil || got != tc.want {
			t.Errorf(
				"exit %d %q: restarts = %d, err = %v, want %d",
				tc.exitCode,
				tc.stdout,
				got,
				err,
				tc.want,
			)
		}
	}

	if _, err := parseYBScratchRestarts(
		t.Context(),
		2,
		"grep: yugabyted.log: No such file or directory",
	); err == nil {
		t.Fatal("a log grep could not read must be an error")
	}
}

// TestYBScratchRestartMarkerMatchesOnlyTheEngineProcesses checks that the
// restart pattern matches only the two processes that restore requires.
// yugabyted also logs the sentence for its web server, which the drill does
// not use. Matching a web server restart would falsely fail a healthy drill.
func TestYBScratchRestartMarkerMatchesOnlyTheEngineProcesses(t *testing.T) {
	marker := regexp.MustCompile(ybScratchRestartMarker)

	for _, line := range []string{
		"[yugabyted start] 2026-09-15 15:08:56,229 ERROR: | 3.5s | master died unexpectedly. Restarting...",
		"[yugabyted start] 2026-09-15 15:08:56,229 ERROR: | 3.5s | tserver died unexpectedly. Restarting...",
	} {
		if !marker.MatchString(line) {
			t.Errorf("marker must match %q", line)
		}
	}

	webserver := "[yugabyted start] 2026-09-15 15:08:56,229 ERROR: | 3.5s | Webserver died unexpectedly. Restarting..."
	if marker.MatchString(webserver) {
		t.Errorf("marker must not match the web server line %q", webserver)
	}
}

// TestYBRestorationFailureNamesATerminalState checks that a master-reported
// FAILED restoration ends the wait. A listing with a RESTORING or RESTORED
// restoration does not end it.
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

// TestYBScratchTabletCountersRefusesAnotherPage checks that the parser
// rejects an HTML error page instead of treating it as tablet progress.
func TestYBScratchTabletCountersRefusesAnotherPage(t *testing.T) {
	if _, err := ybScratchTabletCounters(
		t.Context(),
		"<html>Error retrieving leader master URL</html>",
	); err == nil {
		t.Fatal("an HTML page must not parse as tablet counters")
	}
}
