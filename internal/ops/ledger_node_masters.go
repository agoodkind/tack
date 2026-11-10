// ledger_node_masters.go is the pure half of `ops ledger node-prepare`: what
// the environment says this node's masters are, what the node's saved
// launcher config says they are, and the rewrite that aligns the two.
//
// The launcher (yugabyted) saves the master list it last knew under
// current_masters and hands that list to the tablet server on every start.
// Its own background refresh does not repair a stale list, so a node that
// slept through a master change comes back dialing a master that no longer
// exists and never rejoins (TACK-489). Aligning the saved list before the
// start is what makes the start honest.

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"goodkind.io/tack/internal/config"
)

const (
	// ledgerLauncherConfigDir is where yugabyted keeps its saved config inside
	// the ledger container, under the persisted data volume.
	ledgerLauncherConfigDir = "/home/yugabyte/var/conf"
	// ledgerLauncherConfigName is the saved config's file name.
	ledgerLauncherConfigName = "yugabyted.conf"
	// ledgerLauncherMastersKey is the JSON key holding the comma-separated
	// master list the launcher passes to the tablet server as
	// --tserver_master_addrs.
	ledgerLauncherMastersKey = "current_masters"
	// ledgerMasterRPCPort is the master peer port every node listens on.
	ledgerMasterRPCPort = "7100"
)

// deployedLedgerNodeNames reads the node names out of TACK_LEDGER_NODE_HOSTS,
// the name=address list the deploy renders for every ledger guest. The names
// are the identities the masters carry, which is what the saved list must
// hold; an address there would be the wedge class the node names exist to
// prevent. An empty map is an error: with nothing to align to, the command
// must not guess.
func deployedLedgerNodeNames(cfg *config.Config) ([]string, error) {
	var names []string
	for _, name := range ledgerNodeNames(cfg) {
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil, errors.New("TACK_LEDGER_NODE_HOSTS names no ledger node, so there is no master list to align to")
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// ledgerMasterList renders node names as the name:port entries the launcher
// stores, in a fixed order so two lists compare by content.
func ledgerMasterList(names []string) []string {
	masters := make([]string, 0, len(names))
	for _, name := range names {
		masters = append(masters, name+":"+ledgerMasterRPCPort)
	}
	slices.Sort(masters)
	return masters
}

// savedLedgerMasters reads the master list out of a saved launcher config. A
// config without the key is an error rather than an empty list: the launcher
// writes the key on first start, so its absence means the file is not the one
// this command understands.
func savedLedgerMasters(ctx context.Context, conf []byte) ([]string, error) {
	fields, err := decodeLauncherConfig(ctx, conf)
	if err != nil {
		return nil, err
	}
	raw, present := fields[ledgerLauncherMastersKey]
	if !present {
		return nil, fmt.Errorf("saved launcher config has no %s key", ledgerLauncherMastersKey)
	}
	var joined string
	if err := json.Unmarshal(raw, &joined); err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.config_unreadable", slog.String("err", err.Error()))
		return nil, fmt.Errorf("saved launcher config %s is not a string: %w", ledgerLauncherMastersKey, err)
	}
	var masters []string
	for entry := range strings.SplitSeq(joined, ",") {
		entry = strings.TrimSpace(entry)
		if entry != "" {
			masters = append(masters, entry)
		}
	}
	slices.Sort(masters)
	return slices.Compact(masters), nil
}

// rewriteLedgerMasters returns the saved config with its master list replaced
// and every other key carried through untouched, so the launcher reads back
// exactly the identities and ports it wrote.
func rewriteLedgerMasters(ctx context.Context, conf []byte, masters []string) ([]byte, error) {
	fields, err := decodeLauncherConfig(ctx, conf)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(strings.Join(masters, ","))
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.config_unwritable", slog.String("err", err.Error()))
		return nil, fmt.Errorf("encode master list: %w", err)
	}
	fields[ledgerLauncherMastersKey] = encoded
	rewritten, err := json.MarshalIndent(fields, "", "    ")
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.config_unwritable", slog.String("err", err.Error()))
		return nil, fmt.Errorf("encode saved launcher config: %w", err)
	}
	return append(rewritten, '\n'), nil
}

// decodeLauncherConfig parses the saved config as a flat JSON object, keeping
// each value as the bytes the launcher wrote.
func decodeLauncherConfig(ctx context.Context, conf []byte) (map[string]json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(conf, &fields); err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.config_unreadable", slog.String("err", err.Error()))
		return nil, fmt.Errorf("parse saved launcher config: %w", err)
	}
	return fields, nil
}
