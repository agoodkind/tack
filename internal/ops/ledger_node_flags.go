// The tablet server reads its flag file at startup. The node-prepare command
// compares the mounted file with the server's flags and stops the node when
// they differ. The next docker compose up starts the node with the current file.

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/moby/moby/client"
)

const (
	// ledgerTserverFlagFile specifies the flag file path inside the ledger container.
	ledgerTserverFlagFile = "/home/yugabyte/tserver.flags"
	// ledgerTserverVarzURL specifies the tablet server's local flag endpoint.
	// The compose service publishes the admin port on the guest. tack-ops uses
	// the guest network and connects through localhost.
	ledgerTserverVarzURL = "http://localhost:9000/api/v1/varz"
)

// ybVarz represents the tablet server's /api/v1/varz response.
type ybVarz struct {
	Flags []ybVarzFlag `json:"flags"`
}

// ybVarzFlag represents one flag with its value as a string.
type ybVarzFlag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// staleLedgerNodeFlags returns the flags that differ between the mounted file
// and the tablet server. It skips comparison when the container is not running
// or the flag file does not exist.
func staleLedgerNodeFlags(
	ctx context.Context,
	cli *client.Client,
	containerName string,
) ([]string, error) {
	health, err := inspectLedgerNodeHealth(ctx, cli, containerName)
	if err != nil {
		return nil, err
	}

	if !health.running {
		return nil, nil
	}

	file, found, err := readContainerFile(ctx, cli, containerName, ledgerTserverFlagFile)
	if err != nil {
		return nil, err
	}

	if !found {
		slog.InfoContext(
			ctx,
			"ops.ledger.node_prepare.flag_file_absent",
			slog.String("path", ledgerTserverFlagFile),
		)

		return nil, nil
	}

	body, err := fetchYBMasterHealth(ctx, ledgerTserverVarzURL)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ops.ledger.node_prepare.varz_unreachable",
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("read the tablet server's flags: %w", err)
	}

	serverFlags, err := runningTserverFlags(ctx, body)
	if err != nil {
		return nil, err
	}

	return staleTserverFlags(parseTserverFlagFile(file.content), serverFlags), nil
}

// parseTserverFlagFile reads --name=value lines and skips blank lines and comments.
// A flag without an explicit value gets an empty string.
func parseTserverFlagFile(content []byte) map[string]string {
	flags := map[string]string{}

	for line := range strings.SplitSeq(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		line = strings.TrimPrefix(line, "--")
		name, value, _ := strings.Cut(line, "=")
		flags[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}

	return flags
}

// runningTserverFlags reads the flags the tablet server reports.
func runningTserverFlags(ctx context.Context, body []byte) (map[string]string, error) {
	var listing ybVarz
	if err := json.Unmarshal(body, &listing); err != nil {
		slog.ErrorContext(
			ctx,
			"ops.ledger.node_prepare.varz_unparseable",
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("unmarshal tablet server flags: %w", err)
	}

	flags := make(map[string]string, len(listing.Flags))
	for _, flag := range listing.Flags {
		flags[flag.Name] = flag.Value
	}

	return flags, nil
}

// staleTserverFlags returns sorted file flags with different or missing server values.
func staleTserverFlags(file, serverFlags map[string]string) []string {
	var stale []string

	for name, want := range file {
		if got, ok := serverFlags[name]; !ok || got != want {
			stale = append(stale, name)
		}
	}

	slices.Sort(stale)

	return stale
}
