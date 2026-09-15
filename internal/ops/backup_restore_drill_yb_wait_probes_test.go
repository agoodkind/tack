package ops

import "testing"

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

// TestYBScratchTabletCountersRefusesAnotherPage proves a page that is not the
// tablets page yields an error, so an endpoint that answers with something
// else never vouches for progress.
func TestYBScratchTabletCountersRefusesAnotherPage(t *testing.T) {
	if _, err := ybScratchTabletCounters(t.Context(), "<html>Error retrieving leader master URL</html>"); err == nil {
		t.Fatal("an HTML page must not parse as tablet counters")
	}
}
