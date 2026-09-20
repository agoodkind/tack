package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"

	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/config"
)

// The product store's cluster commands. Each one runs fdbcli in a one-shot
// container that mounts this guest's cluster file, rather than exec-ing into a
// store container, because the guest that orchestrates a change runs no store
// process once the store is on the data guests (TACK-408).

const (
	// storeClusterFileBind gives the one-shot the same cluster file every
	// client on this guest reads. It is writable, because fdbcli records a
	// coordinator change in it.
	storeClusterFileBind = "/etc/foundationdb:/etc/foundationdb"
	// storeClusterFile is the path inside the one-shot.
	storeClusterFile = "/etc/foundationdb/fdb.cluster"
	// storeCLITimeoutSeconds bounds one fdbcli invocation. An unreachable
	// cluster otherwise leaves the one-shot waiting with nothing to report.
	storeCLITimeoutSeconds = 30
	// storeDefaultPort is the port a coordinator address takes when the
	// operator names an address without one.
	storeDefaultPort = 4500
)

// storeRedundancyModes is the closed set of redundancy words. The value
// becomes a word in an fdbcli command, so nothing outside this set reaches it.
var storeRedundancyModes = map[string]bool{"single": true, "double": true, "triple": true}

// storeStorageEngine is the engine every tack cluster is configured with. It
// is not an operator choice: changing it rewrites every storage file.
const storeStorageEngine = "ssd"

// validateRedundancyMode refuses a mode outside the closed set.
func validateRedundancyMode(mode string) error {
	if !storeRedundancyModes[mode] {
		return fmt.Errorf("redundancy mode %q is not one of single, double, triple", mode)
	}
	return nil
}

// parseCoordinatorAddresses turns the operator's comma-separated list into the
// bracketed host:port words fdbcli takes, and refuses anything it cannot read
// as an address. Every address is a guest's pinned address from the service
// inventory: a container address would name a coordinator that disappears on
// the next container recreate, and a name would leave for site DNS, where the
// wildcard record answers as the proxy.
func parseCoordinatorAddresses(list string) ([]string, error) {
	fields := strings.Split(list, ",")
	addresses := make([]string, 0, len(fields))
	for _, field := range fields {
		trimmed := strings.TrimSpace(field)
		if trimmed == "" {
			continue
		}
		address, err := normalizeCoordinatorAddress(trimmed)
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, address)
	}
	if len(addresses) == 0 {
		return nil, errors.New("no coordinator addresses given")
	}
	if len(addresses)%2 == 0 {
		return nil, fmt.Errorf("%d coordinators is an even count; a quorum needs an odd one", len(addresses))
	}
	return addresses, nil
}

// normalizeCoordinatorAddress accepts an IP literal with or without a port and
// returns the bracketed form fdbcli takes.
func normalizeCoordinatorAddress(value string) (string, error) {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		host = strings.Trim(value, "[]")
		port = strconv.Itoa(storeDefaultPort)
	}
	if net.ParseIP(host) == nil {
		return "", fmt.Errorf("coordinator %q is not an IP address; coordinators are the guests' pinned addresses", value)
	}
	portNumber, convErr := strconv.Atoi(port)
	if convErr != nil || portNumber <= 0 || portNumber > 65535 {
		return "", fmt.Errorf("coordinator %q has no usable port", value)
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port, nil
	}
	return host + ":" + port, nil
}

// runStoreCLI runs one fdbcli command against this guest's cluster file and
// returns what it printed. A nonzero exit is an error, because every caller
// here changes cluster configuration and a silent failure would report a
// change that never happened.
func runStoreCLI(ctx context.Context, cfg *config.Config, command string) (string, error) {
	cli, err := newDockerClient(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "ops.store.docker_client_failed",
			slog.String("command", command), slog.String("err", err.Error()))
		return "", fmt.Errorf("store cli docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	return runStoreCLIWith(ctx, cli, cfg, command)
}

// runStoreCLIWith is runStoreCLI against a Docker client the caller owns, so a
// command issuing several fdbcli calls opens one client.
func runStoreCLIWith(ctx context.Context, cli *client.Client, cfg *config.Config, command string) (string, error) {
	output, exitCode, err := readStoreCLI(ctx, cli, cfg, command)
	if err != nil {
		return "", err
	}
	if exitCode != 0 {
		exitErr := fmt.Errorf("store %q exited %d: %s", command, exitCode, output)
		slog.ErrorContext(ctx, "ops.store.cli_nonzero",
			slog.String("command", command), slog.Int("code", exitCode), slog.String("err", exitErr.Error()))
		return output, exitErr
	}
	return output, nil
}

// readStoreCLI runs one fdbcli command and returns what it printed with the
// exit code beside it. The two are separate answers. fdbcli exits nonzero when
// it is asked about a cluster it cannot reach or one that was never
// configured, and that is a reading. The error here is a container that could
// not run at all.
func readStoreCLI(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	command string,
) (output string, exitCode int, err error) {
	res, err := runOneShot(ctx, cli, slog.Default(), runOneShotOptions{
		Image:      cfg.BackupFDBImage,
		Network:    cfg.BackupFDBNetwork,
		Name:       "",
		Entrypoint: []string{"/usr/bin/fdbcli"},
		Cmd: []string{
			"-C", storeClusterFile,
			"--timeout", strconv.Itoa(storeCLITimeoutSeconds),
			"--exec", command,
		},
		Env:        []string{"FDB_CLUSTER_FILE=" + storeClusterFile},
		Binds:      []string{storeClusterFileBind},
		ExtraHosts: nil,
	})
	if err != nil {
		slog.ErrorContext(ctx, "ops.store.cli_failed",
			slog.String("command", command), slog.String("err", err.Error()))
		return "", 0, fmt.Errorf("store %q: %w", command, err)
	}
	return strings.TrimSpace(res.Stdout + res.Stderr), res.ExitCode, nil
}
