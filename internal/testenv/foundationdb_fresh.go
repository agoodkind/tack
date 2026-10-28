package testenv

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// FreshFoundationDB creates an independent cluster and removes only that cluster after the test.
func FreshFoundationDB(t *testing.T) string {
	t.Helper()
	return FreshFoundationDBWithKnobs(t, nil)
}

// FreshFoundationDBWithKnobs creates an independent cluster, sets each server
// knob, name to value, on its fdbserver, and removes only that cluster after
// the test.
func FreshFoundationDBWithKnobs(t *testing.T, knobs map[string]string) string {
	t.Helper()
	skipWhenShort(t)
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	cluster, err := provisionFoundationDBWithKnobs(ctx, knobs)
	if err != nil {
		t.Fatalf("provision fresh FoundationDB cluster: %v", err)
	}
	slog.InfoContext(ctx, "testenv.foundationdb.fresh", slog.String("container", filepath.Base(filepath.Dir(cluster))))
	removeFoundationDBAfterTest(t, cluster)
	return cluster
}

// ConfigureFoundationDB runs command through fdbcli inside the container that
// serves cluster. It fails the test if Docker client creation or the command
// fails.
func ConfigureFoundationDB(t *testing.T, cluster, command string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	cli, err := dockerClient(ctx)
	if err != nil {
		t.Fatalf("Docker client creation failed; expected a client for FoundationDB configuration: %v", err)
	}
	defer func() { _ = cli.Close() }()
	output, exitCode, err := fdbcli(ctx, cli, FoundationDBContainer(cluster), command)
	if err != nil || exitCode != 0 {
		t.Fatalf("fdbcli did not complete successfully; expected exit code 0; command %q exit %d: %v\n%s", command, exitCode, err, output)
	}
}

// removeFoundationDBAfterTest removes one test-owned cluster container and its
// cluster-file directory when the test ends.
func removeFoundationDBAfterTest(t *testing.T, cluster string) {
	t.Helper()
	directory := filepath.Dir(cluster)
	container := filepath.Base(directory)
	slog.InfoContext(t.Context(), "testenv.foundationdb.removal_registered", slog.String("container", container))
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), provisionTimeout)
		defer cancel()
		if err := removeContainers(cleanup, []string{container}); err != nil {
			t.Errorf("remove FoundationDB cluster %s: %v", container, err)
		}
		if err := os.RemoveAll(directory); err != nil {
			t.Errorf("remove FoundationDB cluster file %s: %v", directory, err)
		}
	})
}
