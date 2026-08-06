package foundationdb

import (
	"context"
	"os"
	"testing"
)

func TestStoresPing(t *testing.T) {
	clusterFile := os.Getenv("FDB_CLUSTER_FILE")
	if clusterFile == "" {
		t.Skip("FDB_CLUSTER_FILE is required for FoundationDB integration tests")
	}
	stores, err := NewStores(clusterFile, nil)
	if err != nil {
		t.Fatalf("NewStores: %v", err)
	}
	if err := stores.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}
