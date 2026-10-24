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

const (
	// foundationDBProcessPort is the port every test FoundationDB process
	// listens on.
	foundationDBProcessPort = "4500"
	// foundationDBDataDirectory is the data directory of each process.
	foundationDBDataDirectory = "/var/fdb/data"
	// foundationDBDataMount sizes the memory file system mounted at each
	// data directory. FoundationDB refuses an exclusion that leaves less than
	// 10 percent of the storage space free, and it measures that space on the
	// data directory. On the runner's shared disk, that measurement follows
	// the free space that other tests leave.
	foundationDBDataMount = "size=2g"
	// foundationDBJoiningClass is the process class of every process after
	// the coordinator.
	foundationDBJoiningClass = "storage"
)

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
// process belongs to the cluster and the cluster reports healthy data. The
// test removes every container of the cluster when it ends.
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
	dataMount := map[string]string{foundationDBDataDirectory: foundationDBDataMount}
	coordinator, err := startEngine(ctx, cli, engineSpec{
		kind: "foundationdb", image: image, platform: nil, cmd: nil, env: environment, tmpfs: dataMount,
	})
	if err != nil {
		return cluster, nil, err
	}
	names := []string{coordinator.name}
	cluster.Addresses = append(cluster.Addresses, net.JoinHostPort(coordinator.address, foundationDBProcessPort))
	contents, err := waitForClusterFile(ctx, cli, coordinator.name)
	if err != nil {
		return cluster, names, err
	}
	// Each joining process takes the storage class. The coordinator keeps
	// every other role, so excluding a joining process moves only data.
	joining := slices.Concat(environment, []string{
		"FDB_CLUSTER_FILE_CONTENTS=" + strings.TrimSpace(string(contents)), "FDB_PROCESS_CLASS=" + foundationDBJoiningClass,
	})
	for range processes - 1 {
		started, err := startEngine(ctx, cli, engineSpec{
			kind: "foundationdb", image: image, platform: nil, cmd: nil, env: joining, tmpfs: dataMount,
		})
		if err != nil {
			return cluster, names, err
		}
		names = append(names, started.name)
		cluster.Addresses = append(cluster.Addresses, net.JoinHostPort(started.address, foundationDBProcessPort))
	}
	if err := ensureConfigured(ctx, cli, coordinator.name); err != nil {
		return cluster, names, err
	}
	if err := waitForHealthyCluster(ctx, cli, coordinator.name, processes); err != nil {
		return cluster, names, err
	}
	cluster.ClusterFile, err = writeClusterFile(ctx, coordinator.name, contents)
	return cluster, names, err
}

// foundationDBStatus is the part of `status json` that lists the processes
// and reports the health of the stored data.
type foundationDBStatus struct {
	Cluster struct {
		Processes map[string]json.RawMessage `json:"processes"`
		Data      struct {
			State struct {
				Healthy bool   `json:"healthy"`
				Name    string `json:"name"`
			} `json:"state"`
		} `json:"data"`
	} `json:"cluster"`
}

// waitForHealthyCluster polls `status json` until the cluster reports want
// processes and healthy data, so data distribution is running before a test
// moves data.
func waitForHealthyCluster(ctx context.Context, cli *client.Client, containerName string, want int) error {
	reported, state := 0, ""
	for {
		output, exitCode, err := fdbcli(ctx, cli, containerName, "status json")
		if err != nil {
			return err
		}
		var status foundationDBStatus
		healthy := false
		if exitCode == 0 && json.Unmarshal([]byte(output), &status) == nil {
			reported, state = len(status.Cluster.Processes), status.Cluster.Data.State.Name
			healthy = status.Cluster.Data.State.Healthy
		}
		if reported == want && healthy {
			return nil
		}
		if !sleepOrDone(ctx) {
			return fmt.Errorf("the FoundationDB cluster reported %d of %d processes and data state %q before the deadline",
				reported, want, state)
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
