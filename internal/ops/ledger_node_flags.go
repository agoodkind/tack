// ledger_node_flags.go is the flag half of `ops ledger node-prepare`: the
// tablet server reads its flag file once, at start, so a deploy that fetches a
// changed file leaves the running server on the old values until something
// restarts it. The command compares the mounted file with what the server
// reports and stops the node when they differ, so the deploy's next
// `docker compose up` starts it on the file it was given.

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
	// ledgerTserverFlagFile is where docker-compose.yml mounts the tserver
	// flag file inside the ledger container.
	ledgerTserverFlagFile = "/home/yugabyte/tserver.flags"
	// ledgerTserverVarzURL is the local tablet server's flag listing. The
	// compose service publishes the tserver admin port on the guest, and
	// tack-ops runs on the guest's network, so the loopback name reaches it.
	ledgerTserverVarzURL = "http://localhost:9000/api/v1/varz"
)

// ybVarz is the tablet server's flag listing as /api/v1/varz serves it.
type ybVarz struct {
	Flags []ybVarzFlag `json:"flags"`
}

// ybVarzFlag is one flag in the listing; value is the string form the server
// reports whatever the flag's type.
type ybVarzFlag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// staleLedgerNodeFlags reads the flag file mounted in the named container and
// the flags its tablet server reports, and names the ones that differ. A
// container that is not running, or a stack without the flag file, has nothing
// to restart and reads as current.
func staleLedgerNodeFlags(ctx context.Context, cli *client.Client, containerName string) ([]string, error) {
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
		slog.InfoContext(ctx, "ops.ledger.node_prepare.flag_file_absent", slog.String("path", ledgerTserverFlagFile))
		return nil, nil
	}
	body, err := fetchYBMasterHealth(ctx, ledgerTserverVarzURL)
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.varz_unreachable", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read the tablet server's flags: %w", err)
	}
	running, err := runningTserverFlags(ctx, body)
	if err != nil {
		return nil, err
	}
	return staleTserverFlags(parseTserverFlagFile(file.content), running), nil
}

// parseTserverFlagFile reads the `--name=value` lines of a flag file. Blank
// lines and comments are skipped; a flag without a value reads as empty.
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
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.varz_unparseable", slog.String("err", err.Error()))
		return nil, fmt.Errorf("unmarshal tablet server flags: %w", err)
	}
	flags := make(map[string]string, len(listing.Flags))
	for _, flag := range listing.Flags {
		flags[flag.Name] = flag.Value
	}
	return flags, nil
}

// staleTserverFlags names every flag the file sets that the running server
// does not hold at the file's value, in a fixed order. A flag the server does
// not list at all counts as stale: the file names it, the server never read it.
func staleTserverFlags(file, running map[string]string) []string {
	var stale []string
	for name, want := range file {
		if got, ok := running[name]; !ok || got != want {
			stale = append(stale, name)
		}
	}
	slices.Sort(stale)
	return stale
}
