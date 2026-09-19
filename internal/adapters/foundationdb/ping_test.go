package foundationdb

import (
	"context"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

func TestStoresPing(t *testing.T) {
	stores, err := NewStores(testenv.FoundationDB(t), nil)
	if err != nil {
		t.Fatalf("NewStores: %v", err)
	}
	if err := stores.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}
