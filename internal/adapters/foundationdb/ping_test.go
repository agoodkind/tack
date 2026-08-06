package foundationdb

import (
	"context"
	"errors"
	"os"
	"testing"
)

const defaultFDBClusterFile = "/etc/foundationdb/fdb.cluster"

func TestStoresPing(t *testing.T) {
	clusterFile := fdbClusterFileForPingTest(t)
	stores, err := NewStores(clusterFile, nil)
	if err != nil {
		t.Fatalf("NewStores: %v", err)
	}
	if err := stores.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func fdbClusterFileForPingTest(t *testing.T) string {
	t.Helper()
	candidates := []string{os.Getenv("FDB_CLUSTER_FILE"), defaultFDBClusterFile}
	for _, clusterFile := range candidates {
		if clusterFile == "" {
			continue
		}
		if _, err := os.Stat(clusterFile); err == nil {
			return clusterFile
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat FoundationDB cluster file %q: %v", clusterFile, err)
		}
	}
	t.Skip("FoundationDB cluster file is unavailable")
	return ""
}
