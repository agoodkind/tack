// ledger_node_masters.go updates the saved master list before node startup.
// yugabyted saves current_masters and supplies it to the tablet server at each
// start. Its background refresh does not repair a stale list. A stale entry can
// prevent a restarted node from rejoining after a master change (TACK-489).

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
	// ledgerLauncherConfigDir stores yugabyted's config on the persisted volume.
	ledgerLauncherConfigDir = "/home/yugabyte/var/conf"
	// ledgerLauncherConfigName is the saved config's file name.
	ledgerLauncherConfigName = "yugabyted.conf"
	// ledgerLauncherMastersKey stores the comma-separated master list that
	// the launcher supplies as --tserver_master_addrs.
	ledgerLauncherMastersKey = "current_masters"
	// ledgerMasterRPCPort is the master peer port every node listens on.
	ledgerMasterRPCPort = "7100"
)

// deployedLedgerNodeNames reads names from the deployed name=address entries
// in TACK_LEDGER_NODE_HOSTS. Saved master lists require these stable node names
// rather than addresses. The command refuses an empty list instead of guessing.
func deployedLedgerNodeNames(cfg *config.Config) ([]string, error) {
	var names []string

	for _, name := range ledgerNodeNames(cfg) {
		names = append(names, name)
	}

	if len(names) == 0 {
		return nil, errors.New(
			"TACK_LEDGER_NODE_HOSTS does not specify any ledger nodes for the master list",
		)
	}

	slices.Sort(names)

	return slices.Compact(names), nil
}

// ledgerMasterList renders sorted name:port entries for content comparisons.
func ledgerMasterList(names []string) []string {
	masters := make([]string, 0, len(names))
	for _, name := range names {
		masters = append(masters, name+":"+ledgerMasterRPCPort)
	}

	slices.Sort(masters)

	return masters
}

// savedLedgerMasters reads a saved launcher's master list. The launcher writes
// the required key at first start. Its absence indicates an unsupported config.
func savedLedgerMasters(ctx context.Context, conf []byte) ([]string, error) {
	fields, err := decodeLauncherConfig(ctx, conf)
	if err != nil {
		return nil, err
	}

	raw, present := fields[ledgerLauncherMastersKey]
	if !present {
		return nil, fmt.Errorf("saved launcher config does not include the %s key", ledgerLauncherMastersKey)
	}

	var joined string
	if err := json.Unmarshal(raw, &joined); err != nil {
		slog.ErrorContext(
			ctx,
			"ops.ledger.node_prepare.config_unreadable",
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf(
			"saved launcher config %s is not a string: %w",
			ledgerLauncherMastersKey,
			err,
		)
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

// rewriteLedgerMasters replaces the master list and preserves every other
// config value, including node identities and ports.
func rewriteLedgerMasters(ctx context.Context, conf []byte, masters []string) ([]byte, error) {
	fields, err := decodeLauncherConfig(ctx, conf)
	if err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(strings.Join(masters, ","))
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ops.ledger.node_prepare.config_unwritable",
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("encode master list: %w", err)
	}

	fields[ledgerLauncherMastersKey] = encoded
	rewritten, err := json.MarshalIndent(fields, "", "    ")
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ops.ledger.node_prepare.config_unwritable",
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("encode saved launcher config: %w", err)
	}

	return append(rewritten, '\n'), nil
}

// decodeLauncherConfig parses a flat JSON object and preserves each raw value.
func decodeLauncherConfig(ctx context.Context, conf []byte) (map[string]json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(conf, &fields); err != nil {
		slog.ErrorContext(
			ctx,
			"ops.ledger.node_prepare.config_unreadable",
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("parse saved launcher config: %w", err)
	}

	return fields, nil
}
