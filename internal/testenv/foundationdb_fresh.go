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
	skipWhenShort(t)
	ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
	defer cancel()
	cluster, err := provisionFoundationDB(ctx)
	if err != nil {
		t.Fatalf("provision fresh FoundationDB cluster: %v", err)
	}
	slog.InfoContext(ctx, "testenv.foundationdb.fresh", slog.String("container", filepath.Base(filepath.Dir(cluster))))
	removeFoundationDBAfterTest(t, cluster)
	return cluster
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
