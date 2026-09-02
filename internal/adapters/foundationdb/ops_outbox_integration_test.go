package foundationdb

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
)

// TestOpsOutboxReadAndClear writes separate committed versionstamped events,
// reads them in commit order, and clears through the first mark.
func TestOpsOutboxReadAndClear(t *testing.T) {
	if os.Getenv("TACK_INTEGRATION") == "" {
		t.Skip("integration test: set TACK_INTEGRATION=1 to run")
	}
	clusterFile := os.Getenv("FDB_CLUSTER_FILE")
	if clusterFile == "" {
		clusterFile = defaultFDBClusterFile
	}
	db, err := Open(clusterFile)
	if err != nil {
		t.Fatalf("open fdb: %v", err)
	}
	prefixID := uuid.New()
	prefix := append([]byte("tack-test:"), prefixID[:]...)
	SetTestPrefix(prefix)
	t.Cleanup(func() {
		clearOpsOutboxTestPrefix(t, db, prefix)
		SetTestPrefix(nil)
	})

	store := NewOpsOutboxStore(db)
	ctx := context.Background()
	// Written through the production Append rather than a test-only writer,
	// so the commit-order and clear-through proof below covers the path the
	// producer spill uses.
	for _, event := range []string{`{"verb":"first"}`, `{"verb":"second"}`} {
		if err := store.Append(ctx, json.RawMessage(event)); err != nil {
			t.Fatalf("append %s: %v", event, err)
		}
	}
	entries, err := store.ReadOutboxFrom(ctx, nil, 10)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("outbox entries = %d, want 2", len(entries))
	}
	if bytes.Compare(entries[0].Mark, entries[1].Mark) >= 0 {
		t.Fatal("outbox marks are not in commit order")
	}
	if !bytes.Equal(entries[0].Event, []byte(`{"verb":"first"}`)) {
		t.Fatalf("first event = %s", entries[0].Event)
	}
	if !bytes.Equal(entries[1].Event, []byte(`{"verb":"second"}`)) {
		t.Fatalf("second event = %s", entries[1].Event)
	}

	remaining, err := store.ReadOutboxFrom(ctx, entries[0].Mark, 10)
	if err != nil {
		t.Fatalf("read after first mark: %v", err)
	}
	if len(remaining) != 1 || !bytes.Equal(remaining[0].Event, entries[1].Event) {
		t.Fatalf("entries after first mark = %+v, want second event", remaining)
	}

	if err := store.ClearThrough(ctx, entries[0].Mark); err != nil {
		t.Fatalf("clear through first mark: %v", err)
	}
	remaining, err = store.ReadOutboxFrom(ctx, nil, 10)
	if err != nil {
		t.Fatalf("read after clear: %v", err)
	}
	if len(remaining) != 1 || !bytes.Equal(remaining[0].Event, entries[1].Event) {
		t.Fatalf("entries after clear = %+v, want second event", remaining)
	}
}

func clearOpsOutboxTestPrefix(t *testing.T, db fdb.Database, prefix []byte) {
	t.Helper()
	end := append(append([]byte(nil), prefix...), 0xFF)
	_, err := db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.ClearRange(fdb.KeyRange{Begin: fdb.Key(prefix), End: fdb.Key(end)})
		return nil, nil
	})
	if err != nil {
		t.Logf("cleanup: clear range: %v", err)
	}
}
