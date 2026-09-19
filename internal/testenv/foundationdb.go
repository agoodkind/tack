package testenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/moby/moby/client"
)

const (
	// foundationDBService is the stack file service whose image the cluster
	// runs.
	foundationDBService = "fdb"
	// containerClusterFile is where the image's entrypoint writes the cluster
	// file, naming the server by its address on the engines' network.
	containerClusterFile = "/var/fdb/fdb.cluster"
	// databaseAvailable is what `status minimal` prints once the database
	// serves reads and writes.
	databaseAvailable = "The database is available"
	// databaseExists is what `configure new` prints when the cluster already
	// holds a database, which makes a second configure harmless.
	databaseExists = "Database already exists"
)

// provisionFoundationDB starts this process's cluster, configures it, and
// returns the path of a local copy of its cluster file.
func provisionFoundationDB(ctx context.Context) (string, error) {
	image, err := serviceImage(ctx, foundationDBService)
	if err != nil {
		return "", err
	}
	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()
	started, err := startEngine(ctx, cli, engineSpec{
		kind:     "foundationdb",
		image:    image,
		platform: nil,
		cmd:      nil,
		env:      []string{"FDB_NETWORKING_MODE=container", "FDB_PORT=4500", "FDB_COORDINATOR_PORT=4500"},
	})
	if err != nil {
		return "", err
	}
	clusterFile, err := waitForClusterFile(ctx, cli, started.name)
	if err != nil {
		return "", err
	}
	if err := ensureConfigured(ctx, cli, started.name); err != nil {
		return "", err
	}
	return writeClusterFile(ctx, started.name, clusterFile)
}

// waitForClusterFile polls until the entrypoint has written the cluster file.
func waitForClusterFile(ctx context.Context, cli *client.Client, containerName string) ([]byte, error) {
	for {
		contents, err := readContainerFile(ctx, cli, containerName, containerClusterFile)
		if err == nil && len(strings.TrimSpace(string(contents))) > 0 {
			return contents, nil
		}
		if !sleepOrDone(ctx) {
			return nil, errors.New("the FoundationDB container wrote no cluster file before the deadline")
		}
	}
}

// ensureConfigured waits until the database is available, creating it the
// first time the cluster reports none. `configure new` runs only while status
// says the database is unavailable, on a cluster this process just started,
// and FoundationDB refuses it on a cluster that already holds a database, so
// it never replaces one.
func ensureConfigured(ctx context.Context, cli *client.Client, containerName string) error {
	configured := false
	for {
		status, _, err := fdbcli(ctx, cli, containerName, "status minimal")
		if err != nil {
			return err
		}
		if strings.Contains(status, databaseAvailable) {
			return nil
		}
		if !configured {
			output, exitCode, err := fdbcli(ctx, cli, containerName, "configure new single memory")
			if err != nil {
				return err
			}
			configured = exitCode == 0 || strings.Contains(output, databaseExists)
			slog.DebugContext(ctx, "testenv.foundationdb.configure",
				slog.Bool("configured", configured), slog.String("output", strings.TrimSpace(output)))
		}
		if !sleepOrDone(ctx) {
			return fmt.Errorf("FoundationDB was not available before the deadline; last status: %s", strings.TrimSpace(status))
		}
	}
}

// fdbcli runs one fdbcli command inside the cluster's container, bounded by
// fdbcli's own timeout so an unreachable cluster cannot hang the exec.
func fdbcli(ctx context.Context, cli *client.Client, containerName, command string) (string, int, error) {
	return execInContainer(ctx, cli, containerName, []string{
		"fdbcli", "-C", containerClusterFile, "--timeout", "20", "--exec", command,
	})
}

// writeClusterFile stores the cluster file in a directory of its own under
// the user cache directory, named after the container, and records the
// directory for Release. It lives beyond the process only for cmd/testenv,
// whose operator reads it after the command exits.
func writeClusterFile(ctx context.Context, containerName string, contents []byte) (string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		cacheRoot = os.TempDir()
	}
	directory := filepath.Join(cacheRoot, "tack-testenv", containerName)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		slog.ErrorContext(ctx, "testenv.foundationdb.cluster_file_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("create %s: %w", directory, err)
	}
	ownDirectory(directory)
	path := filepath.Join(directory, "fdb.cluster")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		slog.ErrorContext(ctx, "testenv.foundationdb.cluster_file_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}
