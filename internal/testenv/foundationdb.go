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
	// foundationDBService identifies the stack service that specifies the cluster image.
	foundationDBService = "fdb"
	// containerClusterFile specifies where the image's entrypoint writes the
	// server address on the engines' network.
	containerClusterFile = "/var/fdb/fdb.cluster"
	// databaseAvailable matches the status output when reads and writes work.
	databaseAvailable = "The database is available"
	// databaseExists matches configure new output for an existing database.
	// FoundationDB rejects the command without replacing that database.
	databaseExists = "Database already exists"
	// fdbServerKnobPrefix starts each environment variable that fdbserver
	// reads as a knob at startup.
	fdbServerKnobPrefix = "FDB_KNOB_"
)

// provisionFoundationDB starts this process's cluster, configures it, and
// returns the path of a local copy of its cluster file.
func provisionFoundationDB(ctx context.Context) (string, error) {
	return provisionFoundationDBWithKnobs(ctx, nil)
}

// provisionFoundationDBWithKnobs starts a cluster and passes each server knob
// name and value in knobs to its fdbserver through the environment. It then
// configures the cluster and returns the path of a local copy of its cluster
// file.
func provisionFoundationDBWithKnobs(ctx context.Context, knobs map[string]string) (string, error) {
	image, err := serviceImage(ctx, foundationDBService)
	if err != nil {
		return "", err
	}

	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}

	defer func() { _ = cli.Close() }()
	env := []string{"FDB_NETWORKING_MODE=container", "FDB_PORT=4500", "FDB_COORDINATOR_PORT=4500"}

	for name, value := range knobs {
		env = append(env, fdbServerKnobPrefix+name+"="+value)
	}

	started, err := startEngine(
		ctx,
		cli,
		engineSpec{
			kind:     "foundationdb",
			image:    image,
			platform: nil,
			cmd:      nil,
			env:      env,
		},
	)
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
func waitForClusterFile(
	ctx context.Context,
	cli *client.Client,
	containerName string,
) ([]byte, error) {
	for {
		contents, err := readContainerFile(ctx, cli, containerName, containerClusterFile)
		if err == nil && len(strings.TrimSpace(string(contents))) > 0 {
			return contents, nil
		}

		if !sleepOrDone(ctx) {
			return nil, errors.New(
				"the FoundationDB container did not write a cluster file before the deadline",
			)
		}
	}
}

// ensureConfigured waits for the new test cluster to become available.
// It runs configure new only after status reports an unavailable database.
// FoundationDB rejects configure new if a database already exists.
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
			slog.DebugContext(
				ctx,
				"testenv.foundationdb.configure",
				slog.Bool("configured", configured),
				slog.String("output", strings.TrimSpace(output)),
			)
		}

		if !sleepOrDone(ctx) {
			return fmt.Errorf(
				"FoundationDB was not available before the deadline; last status: %s",
				strings.TrimSpace(status),
			)
		}
	}
}

// fdbcli runs one command inside the cluster's container. The CLI timeout
// bounds execution when the cluster cannot respond.
func fdbcli(
	ctx context.Context,
	cli *client.Client,
	containerName string,
	command string,
) (string, int, error) {
	return execInContainer(
		ctx,
		cli,
		containerName,
		[]string{
			"fdbcli",
			"-C",
			containerClusterFile,
			"--timeout",
			"20",
			"--exec",
			command,
		},
	)
}

// writeClusterFile stores the cluster file under the user cache directory
// in a container-specific directory and records it for Release. cmd/testenv
// preserves the directory for operators to use after the command exits.
func writeClusterFile(ctx context.Context, containerName string, contents []byte) (string, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		cacheRoot = os.TempDir()
	}

	directory := filepath.Join(cacheRoot, "tack-testenv", containerName)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		slog.ErrorContext(
			ctx,
			"testenv.foundationdb.cluster_file_failed",
			slog.String("err", err.Error()),
		)

		return "", fmt.Errorf("create %s: %w", directory, err)
	}

	ownDirectory(directory)
	path := filepath.Join(directory, "fdb.cluster")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		slog.ErrorContext(
			ctx,
			"testenv.foundationdb.cluster_file_failed",
			slog.String("err", err.Error()),
		)

		return "", fmt.Errorf("write %s: %w", path, err)
	}

	return path, nil
}
