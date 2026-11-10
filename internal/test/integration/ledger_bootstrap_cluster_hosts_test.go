package integration

import (
	"fmt"
	"net"
	"strings"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

// clusterNodeNames are the ledger node names in TACK_LEDGER_NODE_HOSTS order.
var clusterNodeNames = []string{"yb1", "yb2", "yb3"}

// clusterNodeHosts returns TACK_LEDGER_NODE_HOSTS for yb1, yb2, and yb3 with
// each node's fixed address on the cluster network. Every address is known
// before any node starts.
func clusterNodeHosts(cluster *testenv.LedgerCluster) string {
	addresses := cluster.NodeAddresses()
	entries := make([]string, 0, len(clusterNodeNames))
	for _, name := range clusterNodeNames {
		entries = append(entries, name+"="+addresses[name])
	}
	return strings.Join(entries, ",")
}

// clusterClosedDatabaseURL returns a keyword DSN that connects to yb1's fixed
// address on a port with no listener.
func clusterClosedDatabaseURL(cluster *testenv.LedgerCluster) string {
	return fmt.Sprintf("host=%s port=1 user=yugabyte dbname=tack sslmode=disable connect_timeout=5",
		cluster.NodeAddresses()["yb1"])
}

// requireNoNodeNameDNS requires every node name lookup in the test process to
// fail. On QA each node runs on its own guest, site DNS answers a bare node
// name with the proxy, and the host-networked ops process resolves no node
// name. The bootstrap wait must work with no node name in DNS.
func requireNoNodeNameDNS(t *testing.T) {
	t.Helper()
	for _, name := range clusterNodeNames {
		addresses, err := net.DefaultResolver.LookupHost(t.Context(), name)
		if err == nil {
			t.Fatalf("the test process resolves node name %s to %v, want no DNS answer", name, addresses)
		}
		t.Logf("node name %s has no DNS answer in the test process: %v", name, err)
	}
}
