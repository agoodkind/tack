package ops

import (
	"slices"
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

// ybTabletServersCapture is list_all_tablet_servers output captured live from
// 2024.2.8.0 on 2026-08-28, copied from the rows of the fixture in
// backup_yb_manifest_test.go: a header and three servers, one of them DEAD.
const ybTabletServersCapture = "Tablet Server UUID               RPC Host/Port Heartbeat delay Status   Reads/s  Writes/s Uptime   SST total size  SST uncomp size SST #files      Memory   Broadcast Host/Port \n" +
	"4f5e4f2de0294c44bc30c15d1e4ce337 yb2:9100 0.77s           ALIVE    0.00     0.20     1514     1.03 GB         9.58 GB         62              189.01 MB yb2:9100\n" +
	"44f39a5171aa432d9d1ed77a234da0d7 yb3:9100 60.54s          DEAD     0.00     0.00     0        1.03 GB         9.58 GB         59              201.66 MB yb3:9100\n" +
	"fb84db84104f43ffb3e5c33d409242ef yb1:9100 0.74s           ALIVE    0.00     0.00     1522     1.03 GB         9.58 GB         63              203.60 MB yb1:9100\n"

// ybMastersCapture and ybUniverseConfigCapture are list_all_masters and
// get_universe_config output from 2024.2.8.0, captured by
// TestLedgerBootstrapCluster on 347dab0b (2026-10-02) after the yb3 join.
const (
	ybMastersCapture = "Master UUID                      \tRPC Host/Port        \tState    \tRole \tBroadcast Host/Port \n" +
		"389623d1a434446795b66e3816501975 \tyb1:7100             \tALIVE    \tLEADER \tyb1:7100            \n" +
		"9671a45b35da496588e36cf92e10e5f6 \tyb2:7100             \tALIVE    \tFOLLOWER \tyb2:7100            \n" +
		"d86ba3792d244ab39f74aa5b71f44ff1 \tyb3:7100             \tALIVE    \tFOLLOWER \tyb3:7100            \n"
	ybUniverseConfigCapture = `{"version":2,"replicationInfo":{"liveReplicas":{"numReplicas":3,"placementBlocks":` +
		`[{"cloudInfo":{"placementCloud":"cloud1","placementRegion":"datacenter1","placementZone":"rack1"},` +
		`"minNumReplicas":3}],"placementUuid":"MjlmMzE0NDAtM2E2OS00YzYxLWE3ZWQtYzlkYjRjOTgxYzJm"}},` +
		`"clusterUuid":"c2ff4bc4-7b9c-4ce4-b68e-fb64b5f2027b","universeUuid":"dbdd9093-dd60-4672-86ca-1a65c6a3ac9a"}`
)

// TestCountAliveRowsCountsLiveTabletServers counts the two ALIVE rows of the
// live capture and skips the header and the DEAD server.
func TestCountAliveRowsCountsLiveTabletServers(t *testing.T) {
	if got := countAliveRows(ybTabletServersCapture); got != 2 {
		t.Fatalf("ALIVE rows = %d, want 2 (yb1 and yb2, with the DEAD yb3 skipped)", got)
	}
}

// TestCountAliveRowsCountsLiveMasters counts the three ALIVE masters of the
// captured list and skips the header.
func TestCountAliveRowsCountsLiveMasters(t *testing.T) {
	if got := countAliveRows(ybMastersCapture); got != 3 {
		t.Fatalf("ALIVE masters = %d, want 3", got)
	}
}

// TestUnmarshalUniverseNumReplicasReadsTheCapture reads numReplicas 3 from
// the captured universe config.
func TestUnmarshalUniverseNumReplicasReadsTheCapture(t *testing.T) {
	replicas, err := unmarshalUniverseNumReplicas(t.Context(), ybUniverseConfigCapture)
	if err != nil || replicas != 3 {
		t.Fatalf("numReplicas = %d, %v, want 3", replicas, err)
	}
}

// TestLedgerBootstrapMasterListDialsNamesAndAddresses requires the first two
// nodes in name order: yb-admin names with one hosts entry each, and
// bracketed addresses for the health check.
func TestLedgerBootstrapMasterListDialsNamesAndAddresses(t *testing.T) {
	cfg := &config.Config{LedgerNodeHosts: "yb3=fd00::3,yb1=fd00::1,yb2=fd00::2"}
	masters, err := ledgerBootstrapMasterList(cfg, 2)
	if err != nil {
		t.Fatalf("master list: %v", err)
	}
	if masters.names != "yb1:7100,yb2:7100" {
		t.Errorf("names = %q, want yb1:7100,yb2:7100", masters.names)
	}
	if !slices.Equal(masters.extraHosts, []string{"yb1:fd00::1", "yb2:fd00::2"}) {
		t.Errorf("extra hosts = %v, want yb1:fd00::1 and yb2:fd00::2", masters.extraHosts)
	}
	if masters.health != "[fd00::1]:7100,[fd00::2]:7100" {
		t.Errorf("health = %q, want [fd00::1]:7100,[fd00::2]:7100", masters.health)
	}
	if _, err := ledgerBootstrapMasterList(cfg, 4); err == nil {
		t.Error("a count above the node list was accepted")
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
