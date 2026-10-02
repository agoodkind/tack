// ledger_bootstrap_wait_reading.go is the parsing half of `ops ledger
// bootstrap-wait`: the master address list it asks, the parsing of yb-admin
// output, and the decision whether one reading satisfies the target.

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/config"
)

// ledgerAliveWord matches the status yb-admin prints for a live master or
// tablet server as a whole word.
var ledgerAliveWord = regexp.MustCompile(`\bALIVE\b`)

// ledgerBootstrapReading is one poll of the cluster. Each count is valid only
// when its read flag is true.
type ledgerBootstrapReading struct {
	masters             int
	mastersRead         bool
	tabletServers       int
	tabletServersRead   bool
	replicas            int
	replicasRead        bool
	underReplicated     int
	underReplicatedRead bool
	lastError           string
}

// ledgerBootstrapMasters lists the first nodes of TACK_LEDGER_NODE_HOSTS in
// the two forms the wait dials. yb-admin dials node names, the identity
// ledger TLS verifies, and resolves each name through an extra hosts entry:
// site DNS answers a bare node name with the proxy. The master health check
// runs in the host-networked ops process, which resolves no node name, and
// dials the pinned addresses.
type ledgerBootstrapMasters struct {
	// names is the yb-admin master list, name:7100 entries comma joined.
	names string
	// extraHosts is one name:address entry per node for the one-shot.
	extraHosts []string
	// health is the health check list, [address]:7100 entries comma joined.
	health string
}

// ledgerBootstrapMasterList reads the first count nodes of
// TACK_LEDGER_NODE_HOSTS in node name order. It never uses
// TACK_BACKUP_YB_MASTER_ADDRESSES: on a bootstrap run that list contains
// nodes that have not started yet.
func ledgerBootstrapMasterList(cfg *config.Config, count int) (ledgerBootstrapMasters, error) {
	names, err := deployedLedgerNodeNames(cfg)
	if err != nil {
		return ledgerBootstrapMasters{}, err
	}
	if count > len(names) {
		return ledgerBootstrapMasters{}, fmt.Errorf("--masters %d exceeds the %d ledger nodes in TACK_LEDGER_NODE_HOSTS", count, len(names))
	}
	addressOf := map[string]string{}
	for address, name := range ledgerNodeNames(cfg) {
		if earlier, seen := addressOf[name]; seen {
			return ledgerBootstrapMasters{}, fmt.Errorf("TACK_LEDGER_NODE_HOSTS gives node %s two addresses, %s and %s", name, earlier, address)
		}
		addressOf[name] = address
	}
	masterNames := make([]string, 0, count)
	extraHosts := make([]string, 0, count)
	health := make([]string, 0, count)
	for _, name := range names[:count] {
		masterNames = append(masterNames, name+":"+ledgerMasterRPCPort)
		extraHosts = append(extraHosts, name+":"+addressOf[name])
		health = append(health, net.JoinHostPort(addressOf[name], ledgerMasterRPCPort))
	}
	return ledgerBootstrapMasters{
		names: strings.Join(masterNames, ","), extraHosts: extraHosts, health: strings.Join(health, ","),
	}, nil
}

// countAliveRows counts the output lines of list_all_masters or
// list_all_tablet_servers that contain the whole word ALIVE.
func countAliveRows(output string) int {
	rows := 0
	for line := range strings.SplitSeq(output, "\n") {
		if ledgerAliveWord.MatchString(line) {
			rows++
		}
	}
	return rows
}

// ybUniverseConfig is the part of get_universe_config the wait reads. Every
// level is a pointer: a missing key is a failed read, never a count of 0.
type ybUniverseConfig struct {
	ReplicationInfo *struct {
		LiveReplicas *struct {
			NumReplicas *int `json:"numReplicas"`
		} `json:"liveReplicas"`
	} `json:"replicationInfo"`
}

// unmarshalUniverseNumReplicas reads replicationInfo.liveReplicas.numReplicas
// from get_universe_config output, decoding the text from the first { to the
// last }.
func unmarshalUniverseNumReplicas(ctx context.Context, output string) (int, error) {
	start, end := strings.Index(output, "{"), strings.LastIndex(output, "}")
	if start < 0 || end < start {
		return 0, errors.New("get_universe_config printed no JSON object")
	}
	var universe ybUniverseConfig
	if err := json.Unmarshal([]byte(output[start:end+1]), &universe); err != nil {
		wrapped := fmt.Errorf("decode get_universe_config: %w", err)
		slog.WarnContext(ctx, "ops.ledger.bootstrap_wait.universe_unreadable", slog.String("err", wrapped.Error()))
		return 0, wrapped
	}
	if universe.ReplicationInfo == nil || universe.ReplicationInfo.LiveReplicas == nil ||
		universe.ReplicationInfo.LiveReplicas.NumReplicas == nil {
		return 0, errors.New("get_universe_config has no replicationInfo.liveReplicas.numReplicas")
	}
	return *universe.ReplicationInfo.LiveReplicas.NumReplicas, nil
}

// satisfies reports whether the reading matches the target exactly.
func (r ledgerBootstrapReading) satisfies(target ledgerBootstrapTarget) bool {
	if !r.mastersRead || r.masters != target.masters {
		return false
	}
	if !r.tabletServersRead || r.tabletServers != target.tabletServers {
		return false
	}
	if target.replicas == 0 {
		return true
	}
	return r.replicasRead && r.replicas == target.replicas && r.underReplicatedRead && r.underReplicated == 0
}

// summary renders every count of the reading. A count with a failed read
// renders as unreadable.
func (r ledgerBootstrapReading) summary(target ledgerBootstrapTarget) string {
	parts := []string{
		"masters=" + readCount(r.masters, r.mastersRead),
		"tablet_servers=" + readCount(r.tabletServers, r.tabletServersRead),
	}
	if target.replicas > 0 {
		parts = append(parts,
			"num_replicas="+readCount(r.replicas, r.replicasRead),
			"under_replicated_tablets="+readCount(r.underReplicated, r.underReplicatedRead))
	} else {
		parts = append(parts, "num_replicas=not checked", "under_replicated_tablets=not checked")
	}
	lastError := r.lastError
	if lastError == "" {
		lastError = "none"
	}
	parts = append(parts, "last_read_error="+lastError)
	return strings.Join(parts, " ")
}

// readCount renders one count, or unreadable when its read failed.
func readCount(value int, read bool) string {
	if !read {
		return "unreadable"
	}
	return strconv.Itoa(value)
}
