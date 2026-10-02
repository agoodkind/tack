package ops

import (
	"strings"
	"testing"
)

// ybTabletServersCapture is list_all_tablet_servers output captured live from
// 2024.2.8.0 on 2026-08-28, copied from the rows of the fixture in
// backup_yb_manifest_test.go: a header and three servers, one of them DEAD.
const ybTabletServersCapture = "Tablet Server UUID               RPC Host/Port Heartbeat delay Status   Reads/s  Writes/s Uptime   SST total size  SST uncomp size SST #files      Memory   Broadcast Host/Port \n" +
	"4f5e4f2de0294c44bc30c15d1e4ce337 yb2:9100 0.77s           ALIVE    0.00     0.20     1514     1.03 GB         9.58 GB         62              189.01 MB yb2:9100\n" +
	"44f39a5171aa432d9d1ed77a234da0d7 yb3:9100 60.54s          DEAD     0.00     0.00     0        1.03 GB         9.58 GB         59              201.66 MB yb3:9100\n" +
	"fb84db84104f43ffb3e5c33d409242ef yb1:9100 0.74s           ALIVE    0.00     0.00     1522     1.03 GB         9.58 GB         63              203.60 MB yb1:9100\n"

// TestCountAliveRowsCountsLiveTabletServers counts the two ALIVE rows of the
// live capture and skips the header and the DEAD server.
func TestCountAliveRowsCountsLiveTabletServers(t *testing.T) {
	if got := countAliveRows(ybTabletServersCapture); got != 2 {
		t.Fatalf("ALIVE rows = %d, want 2 (yb1 and yb2, with the DEAD yb3 skipped)", got)
	}
}

// TestUnmarshalUniverseNumReplicasRefusesAMissingKey requires a universe
// config without replicationInfo.liveReplicas.numReplicas to fail the read
// rather than report 0 replicas.
func TestUnmarshalUniverseNumReplicasRefusesAMissingKey(t *testing.T) {
	for _, output := range []string{"{}", `{"replicationInfo":{"liveReplicas":{}}}`, "no object"} {
		replicas, err := unmarshalUniverseNumReplicas(t.Context(), output)
		if err == nil {
			t.Errorf("unmarshalUniverseNumReplicas(%q) = %d, want an error for the missing key", output, replicas)
		}
	}
}

// TestLedgerBootstrapWaitSettingsRequireTheCounts refuses a wait without
// --masters or --tablet-servers and selects the replication deadline when
// --replicas is set.
func TestLedgerBootstrapWaitSettingsRequireTheCounts(t *testing.T) {
	missing := ledgerBootstrapWaitInput{Masters: 0, TabletServers: 0, Replicas: 0, Deadline: "", Poll: "2s"}
	_, _, _, err := ledgerBootstrapWaitSettings(missing)
	if err == nil || !strings.Contains(err.Error(), "--masters") || !strings.Contains(err.Error(), "--tablet-servers") {
		t.Fatalf("settings without counts = %v, want both flags refused", err)
	}
	replicated := ledgerBootstrapWaitInput{Masters: 3, TabletServers: 3, Replicas: 3, Deadline: "", Poll: "2s"}
	_, deadline, _, err := ledgerBootstrapWaitSettings(replicated)
	if err != nil || deadline != ledgerBootstrapReplicationDeadline {
		t.Fatalf("settings with --replicas = %s, %v, want the %s replication deadline",
			deadline, err, ledgerBootstrapReplicationDeadline)
	}
}
