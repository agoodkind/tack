package testenv

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

// foundationDBProcessPort is the port every test FoundationDB process listens
// on.
const foundationDBProcessPort = "4500"

// FoundationDBProcesses is a FoundationDB cluster of several processes that
// one test started.
type FoundationDBProcesses struct {
	// ClusterFile is the path of a local copy of the cluster file.
	ClusterFile string
	// Addresses are the processes' IP:port addresses on the engines' network.
	// The first process is the only coordinator.
	Addresses []string
}

// FoundationDBCluster starts a FoundationDB cluster with the given number of
// processes, configures it with single redundancy, and waits until every
// process belongs to the cluster. The test removes every container of the
// cluster when it ends.
func FoundationDBCluster(t *testing.T, processes int) FoundationDBProcesses {
	t.Helper()
	skipWhenShort(t)
	if processes < 1 {
		t.Fatalf("FoundationDBCluster needs at least one process, got %d", processes)
	}
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	image, err := serviceImage(ctx, foundationDBService)
	if err != nil {
		t.Fatalf("read the FoundationDB image: %v", err)
	}
	cli, err := dockerClient(ctx)
	if err != nil {
		t.Fatalf("create a Docker client for the FoundationDB cluster: %v", err)
	}
	defer func() { _ = cli.Close() }()
	cluster, names, err := startFoundationDBProcesses(ctx, cli, image, processes)
	removeFoundationDBClusterAfterTest(t, names, cluster.ClusterFile)
	if err != nil {
		t.Fatalf("start the FoundationDB cluster: %v", err)
	}
	slog.InfoContext(ctx, "testenv.foundationdb_cluster.ready",
		slog.String("container", names[0]), slog.Int("processes", processes))
	return cluster
}

// startFoundationDBProcesses starts the coordinator, then each other process
// with the coordinator's cluster file, and configures the database. It returns
// the names of every container it started, also on failure.
func startFoundationDBProcesses(
	ctx context.Context, cli *client.Client, image string, processes int,
) (FoundationDBProcesses, []string, error) {
	cluster := FoundationDBProcesses{ClusterFile: "", Addresses: nil}
	environment := []string{"FDB_NETWORKING_MODE=container", "FDB_PORT=" + foundationDBProcessPort}
	coordinator, err := startEngine(ctx, cli, engineSpec{kind: "foundationdb", image: image, platform: nil, cmd: nil, env: environment})
	if err != nil {
		return cluster, nil, err
	}
	names := []string{coordinator.name}
	cluster.Addresses = append(cluster.Addresses, net.JoinHostPort(coordinator.address, foundationDBProcessPort))
	contents, err := waitForClusterFile(ctx, cli, coordinator.name)
	if err != nil {
		return cluster, names, err
	}
	joining := slices.Concat(environment, []string{"FDB_CLUSTER_FILE_CONTENTS=" + strings.TrimSpace(string(contents))})
	for range processes - 1 {
		started, err := startEngine(ctx, cli, engineSpec{kind: "foundationdb", image: image, platform: nil, cmd: nil, env: joining})
		if err != nil {
			return cluster, names, err
		}
		names = append(names, started.name)
		cluster.Addresses = append(cluster.Addresses, net.JoinHostPort(started.address, foundationDBProcessPort))
	}
	if err := ensureConfigured(ctx, cli, coordinator.name); err != nil {
		return cluster, names, err
	}
	if err := waitForProcessCount(ctx, cli, coordinator.name, processes); err != nil {
		return cluster, names, err
	}
	cluster.ClusterFile, err = writeClusterFile(ctx, coordinator.name, contents)
	return cluster, names, err
}

// foundationDBStatus is the part of `status json` that lists the processes.
type foundationDBStatus struct {
	Cluster struct {
		Processes map[string]json.RawMessage `json:"processes"`
	} `json:"cluster"`
}

// waitForProcessCount polls `status json` until the cluster reports want
// processes.
func waitForProcessCount(ctx context.Context, cli *client.Client, containerName string, want int) error {
	reported := 0
	for {
		output, exitCode, err := fdbcli(ctx, cli, containerName, "status json")
		if err != nil {
			return err
		}
		var status foundationDBStatus
		if exitCode == 0 && json.Unmarshal([]byte(output), &status) == nil {
			reported = len(status.Cluster.Processes)
		}
		if reported == want {
			return nil
		}
		if !sleepOrDone(ctx) {
			return fmt.Errorf("the FoundationDB cluster reported %d processes instead of %d before the deadline", reported, want)
		}
	}
}

// removeFoundationDBClusterAfterTest removes the cluster's containers and the
// directory of its cluster file when the test ends.
func removeFoundationDBClusterAfterTest(t *testing.T, names []string, clusterFile string) {
	t.Helper()
	slog.InfoContext(t.Context(), "testenv.foundationdb_cluster.removal_registered", slog.String("containers", strings.Join(names, ",")))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), provisionTimeout)
		defer cancel()
		if err := removeContainers(cleanup, names); err != nil {
			t.Errorf("remove FoundationDB cluster containers %v: %v", names, err)
		}
		if clusterFile == "" {
			return
		}
		if err := os.RemoveAll(filepath.Dir(clusterFile)); err != nil {
			t.Errorf("remove FoundationDB cluster file %s: %v", clusterFile, err)
		}
	})
}
