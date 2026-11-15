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

// Cluster commands run fdbcli in one-shot containers with this guest's cluster
// file. They do not require a local store process; data guests run the store
// after relocation (TACK-408).

const (
	// storeClusterMountPath mounts the host cluster directory. Write access
	// permits fdbcli to save coordinator changes for clients on this guest.
	storeClusterMountPath = "/etc/foundationdb"
	// storeClusterFile is the path inside the one-shot.
	storeClusterFile = storeClusterMountPath + "/fdb.cluster"
	// storeCLITimeoutSeconds limits waiting for an unavailable cluster.
	storeCLITimeoutSeconds = 30
	// storeDefaultPort applies when an address omits its port.
	storeDefaultPort = 4500
)

// storeRedundancyModes restricts values interpolated into fdbcli commands.
var storeRedundancyModes = map[string]bool{"single": true, "double": true, "triple": true}

// storeStorageEngine specifies the required storage engine. Changing it
// rewrites every storage file; operators cannot select another engine here.
const storeStorageEngine = "ssd"

// validateRedundancyMode refuses a mode outside the accepted set.
func validateRedundancyMode(mode string) error {
	if !storeRedundancyModes[mode] {
		return fmt.Errorf("redundancy mode %q is not one of single, double, triple", mode)
	}

	return nil
}

// parseCoordinatorAddresses validates a comma-separated list and returns fdbcli
// host:port values. Addresses must identify guests from the service inventory.
// Container addresses change on recreation, and site DNS resolves node names
// to the proxy through a wildcard record.
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
		return nil, fmt.Errorf(
			"%d coordinators is an even count; a quorum needs an odd one",
			len(addresses),
		)
	}

	return addresses, nil
}

// normalizeCoordinatorAddress accepts an IP literal with an optional port and
// returns the bracketed form fdbcli accepts.
func normalizeCoordinatorAddress(value string) (string, error) {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		host = strings.Trim(value, "[]")
		port = strconv.Itoa(storeDefaultPort)
	}

	if net.ParseIP(host) == nil {
		return "", fmt.Errorf(
			"coordinator %q is not an IP address; coordinators are the guests' pinned addresses",
			value,
		)
	}

	portNumber, convErr := strconv.Atoi(port)
	if convErr != nil || portNumber <= 0 || portNumber > 65535 {
		return "", coordinatorPortMissingError{Address: value}
	}

	if strings.Contains(host, ":") {
		return "[" + host + "]:" + port, nil
	}

	return host + ":" + port, nil
}

// runStoreCLI runs fdbcli against this guest's cluster file and returns its
// output. A nonzero exit returns an error rather than reporting a successful
// configuration change.
func runStoreCLI(ctx context.Context, cfg *config.Config, command string) (string, error) {
	cli, err := newDockerClient(ctx)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ops.store.docker_client_failed",
			slog.String("command", command),
			slog.String("err", err.Error()),
		)

		return "", fmt.Errorf("store cli docker client: %w", err)
	}

	defer func() { _ = cli.Close() }()

	return runStoreCLIWith(ctx, cli, cfg, command)
}

// runStoreCLIWith runs a command using a caller-owned Docker client.
func runStoreCLIWith(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	command string,
) (string, error) {
	output, exitCode, err := readStoreCLI(ctx, cli, cfg, command)
	if err != nil {
		return "", err
	}

	if exitCode != 0 {
		exitErr := fmt.Errorf("store %q exited %d: %s", command, exitCode, output)
		slog.ErrorContext(
			ctx,
			"ops.store.cli_nonzero",
			slog.String("command", command),
			slog.Int("code", exitCode),
			slog.String("err", exitErr.Error()),
		)

		return output, exitErr
	}

	return output, nil
}

// readStoreCLI returns fdbcli output and exit status separately. An unavailable
// or unconfigured cluster can produce a nonzero status. Only a failure to run
// the container returns an error here.
func readStoreCLI(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	command string,
) (output string, exitCode int, err error) {
	res, err := runOneShot(ctx, cli, slog.Default(), storeCLIOptions(cfg, command))
	if err != nil {
		slog.ErrorContext(
			ctx,
			"ops.store.cli_failed",
			slog.String("command", command),
			slog.String("err", err.Error()),
		)

		return "", 0, fmt.Errorf("store %q: %w", command, err)
	}

	return strings.TrimSpace(res.Stdout + res.Stderr), res.ExitCode, nil
}

// storeCLIOptions builds a one-shot command using TACK_OPS_FDB_CLUSTER_DIR.
func storeCLIOptions(cfg *config.Config, command string) runOneShotOptions {
	return runOneShotOptions{
		Image:      cfg.BackupFDBImage,
		Network:    cfg.BackupFDBNetwork,
		Name:       "",
		Entrypoint: []string{"/usr/bin/fdbcli"},
		Cmd: []string{
			"-C",
			storeClusterFile,
			"--timeout",
			strconv.Itoa(storeCLITimeoutSeconds),
			"--exec",
			command,
		},
		Env:        []string{"FDB_CLUSTER_FILE=" + storeClusterFile},
		Binds:      []string{cfg.OpsFDBClusterDir + ":" + storeClusterMountPath},
		ExtraHosts: nil,
	}
}

// fdbClusterReadOnlyBind mounts TACK_OPS_FDB_CLUSTER_DIR at /etc/foundationdb
// without write access for fdbbackup commands that only read the cluster file.
func fdbClusterReadOnlyBind(cfg *config.Config) string {
	return cfg.OpsFDBClusterDir + ":" + storeClusterMountPath + ":ro"
}

type coordinatorPortMissingError struct {
	Address string
}

func (failure coordinatorPortMissingError) Error() string {
	return fmt.Sprintf("coordinator %q does not specify a usable port", failure.Address)
}
