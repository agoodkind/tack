package integration_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

// storeClusterDirectory copies the cluster file into a directory that the
// Docker daemon can bind-mount. It returns the directory and the connection
// string without surrounding whitespace.
func storeClusterDirectory(t *testing.T, clusterFile string) (string, string) {
	t.Helper()
	contents, err := os.ReadFile(clusterFile)
	if err != nil {
		t.Fatalf("Reading the cluster file failed; the store command requires a readable file at %s: %v", clusterFile, err)
	}
	directory := testenv.SharedDir(t)
	if err := os.WriteFile(filepath.Join(directory, "fdb.cluster"), contents, 0o600); err != nil {
		t.Fatalf("Copying the cluster file failed; the store command requires a copy in the shared directory: %v", err)
	}
	return directory, strings.TrimSpace(string(contents))
}

// coordinatorAddress resolves the first coordinator's hostname from a
// FoundationDB connection string and returns its first IP address with the
// coordinator port.
func coordinatorAddress(t *testing.T, connection string) string {
	t.Helper()
	_, coordinators, found := strings.Cut(connection, "@")
	if !found {
		t.Fatalf("The FoundationDB connection string does not contain @; the test requires it before the coordinator addresses in %q", connection)
	}
	host, port, err := net.SplitHostPort(strings.Split(coordinators, ",")[0])
	if err != nil {
		t.Fatalf("Parsing the first coordinator address failed; the test requires a host and port in %q: %v", coordinators, err)
	}
	addresses, err := net.LookupHost(host)
	if err != nil || len(addresses) == 0 {
		t.Fatalf("Coordinator lookup failed or returned an empty address list; the test requires an IP address for host %s: %v", host, err)
	}
	return net.JoinHostPort(addresses[0], port)
}

// storeCommand prepares arguments and copies the selected cluster file into a
// directory that the Docker daemon can bind-mount. It configures the directory
// and Docker network for the store command's fdbcli container.
func storeCommand(
	cluster func(*testing.T, *coverageEnv) string, args func(connection string, t *testing.T) []string,
) func(*testing.T, *coverageEnv) commandInput {
	return func(t *testing.T, env *coverageEnv) commandInput {
		clusterFile := cluster(t, env)
		directory, connection := storeClusterDirectory(t, clusterFile)
		network := env.backup.cfg.BackupFDBNetwork
		return commandInput{args: args(connection, t), outbox: nil, configure: func(cfg *config.Config) {
			cfg.OpsFDBClusterDir, cfg.BackupFDBNetwork = directory, network
		}}
	}
}

// sharedCluster returns the cluster file that the test server uses.
func sharedCluster(_ *testing.T, env *coverageEnv) string {
	return env.server.clusterFile
}

// freshCluster starts a separate FoundationDB cluster and returns its cluster
// file.
func freshCluster(t *testing.T, _ *coverageEnv) string {
	t.Helper()
	return testenv.FreshFoundationDB(t)
}

// migratableCluster starts a separate FoundationDB cluster, enables aggressive
// storage migration, and returns its cluster file.
func migratableCluster(t *testing.T, _ *coverageEnv) string {
	t.Helper()
	clusterFile := testenv.FreshFoundationDB(t)
	testenv.ConfigureFoundationDB(t, clusterFile, "configure storage_migration_type=aggressive")
	return clusterFile
}

// addStoreCommandCases adds FoundationDB store command cases with their
// expected audit verbs and outcomes. The exclude case expects the command to
// fail when it targets the cluster's coordinator.
func addStoreCommandCases(cases map[string]coverageCase) {
	cases["command ops store status"] = commandCase(audit.VerbOpsStoreStatus, audit.OutcomeOK,
		storeCommand(sharedCluster, func(string, *testing.T) []string { return []string{"ops", "store", "status"} }))
	cases["command ops store set-redundancy"] = commandCase(audit.VerbOpsStoreSetRedundancy, audit.OutcomeOK,
		storeCommand(migratableCluster, func(string, *testing.T) []string {
			return []string{"ops", "store", "set-redundancy", "--mode", "single"}
		}))
	cases["command ops store set-coordinators"] = commandCase(audit.VerbOpsStoreSetCoordinators, audit.OutcomeOK,
		storeCommand(freshCluster, func(connection string, t *testing.T) []string {
			return []string{"ops", "store", "set-coordinators", "--addresses", coordinatorAddress(t, connection)}
		}))
	cases["command ops store exclude"] = failingCommandCase(audit.VerbOpsStoreExclude, audit.OutcomeError,
		storeCommand(freshCluster, func(connection string, t *testing.T) []string {
			return []string{"ops", "store", "exclude", "--addresses", coordinatorAddress(t, connection)}
		}))
}
