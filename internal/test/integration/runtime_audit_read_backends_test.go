package integration_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

// readBackend is one read lifecycle store with the test controls that move
// its time and read its outbox.
type readBackend struct {
	name  string
	store audit.ReadLifecycle
	// expire makes the lease and admission deadlines of eventIDs pass.
	expire func(t *testing.T, eventIDs ...uuid.UUID)
	// agePublished moves every publication time past the receipt retention.
	// It is nil for a store that cannot move publication times.
	agePublished func(t *testing.T)
	// terminal returns the outbox events of eventID.
	terminal func(t *testing.T, eventID uuid.UUID) []audit.Event
	// childEnv returns the environment a child process uses to open the store.
	childEnv func() []string
}

// readBackends returns the FoundationDB and YugabyteDB read stores.
func readBackends(t *testing.T) []*readBackend {
	t.Helper()
	return []*readBackend{foundationDBReadBackend(t), yugabyteReadBackend(t)}
}

// foundationDBReadBackend opens the product read store under a new test
// prefix with the wall clock. Its expire control waits in real time, and it
// cannot move publication times.
func foundationDBReadBackend(t *testing.T) *readBackend {
	t.Helper()
	clusterFile := testenv.FoundationDB(t)
	identity := uuid.New()
	prefix := append([]byte("tack-test:"), identity[:]...)
	fdbadapter.SetTestPrefix(prefix)
	stores, err := fdbadapter.NewStores(clusterFile, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("open FoundationDB stores: %v", err)
	}
	t.Cleanup(func() { clearReadPrefix(t, clusterFile, prefix) })
	return &readBackend{
		name:  "foundationdb",
		store: stores.AuditReads(clock.Wall{}),
		expire: func(t *testing.T, _ ...uuid.UUID) {
			t.Helper()
			waitForReadLeases(t)
		},
		agePublished: nil,
		terminal: func(t *testing.T, eventID uuid.UUID) []audit.Event {
			t.Helper()
			entries, err := stores.OpsOutbox.ReadOutboxFrom(t.Context(), nil, 10000)
			if err != nil {
				t.Fatalf("read the FoundationDB outbox: %v", err)
			}
			messages := make([][]byte, 0, len(entries))
			for _, entry := range entries {
				messages = append(messages, entry.Event)
			}
			return transportEvents(t, messages, eventID)
		},
		childEnv: func() []string {
			return []string{
				readChildBackendVariable + "=foundationdb",
				readChildClusterVariable + "=" + clusterFile,
				readChildPrefixVariable + "=" + hex.EncodeToString(prefix),
			}
		},
	}
}

// clearReadPrefix removes every key under the test prefix and clears it.
func clearReadPrefix(t *testing.T, clusterFile string, prefix []byte) {
	t.Helper()
	defer fdbadapter.SetTestPrefix(nil)
	database, err := fdbadapter.Open(clusterFile, 5*time.Second)
	if err != nil {
		t.Logf("cleanup: open FoundationDB: %v", err)
		return
	}
	end := append(bytes.Clone(prefix), 0xFF)
	if _, err := database.Transact(func(tr fdb.Transaction) (any, error) {
		tr.ClearRange(fdb.KeyRange{Begin: fdb.Key(prefix), End: fdb.Key(end)})
		return nil, nil
	}); err != nil {
		t.Logf("cleanup: clear the test prefix: %v", err)
	}
}

// transportEvents decodes the part and manifest messages of eventID and
// returns one event for each manifest.
func transportEvents(t *testing.T, messages [][]byte, eventID uuid.UUID) []audit.Event {
	t.Helper()
	type transportMessage struct {
		Message string    `json:"audit_message"`
		EventID uuid.UUID `json:"event_id"`
		Payload []byte    `json:"payload"`
	}
	var envelope []byte
	var events []audit.Event
	for _, raw := range messages {
		var message transportMessage
		if err := json.Unmarshal(raw, &message); err != nil || message.EventID != eventID {
			continue
		}
		if message.Message == "event_part" {
			envelope = append(envelope, message.Payload...)
			continue
		}
		var decoded struct {
			Event audit.Event `json:"event"`
		}
		if err := json.Unmarshal(envelope, &decoded); err != nil {
			t.Fatalf("decode the envelope of read %s: %v", eventID, err)
		}
		events = append(events, decoded.Event)
		envelope = nil
	}
	return events
}

// yugabyteReadBackend opens the operator read store as the application role.
func yugabyteReadBackend(t *testing.T) *readBackend {
	t.Helper()
	dsn := testenv.Ledger(t)
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open the administrator pool: %v", err)
	}
	t.Cleanup(admin.Close)
	application := applicationRolePool(t, dsn)
	return &readBackend{
		name:  "yugabytedb",
		store: audit.NewPoolReadLifecycle(application),
		expire: func(t *testing.T, eventIDs ...uuid.UUID) {
			t.Helper()
			if _, err := admin.Exec(t.Context(), readExpireLeaseStatement, eventIDs); err != nil {
				t.Fatalf("expire the read leases: %v", err)
			}
		},
		agePublished: func(t *testing.T) {
			t.Helper()
			if _, err := admin.Exec(t.Context(), readAgePublishedStatement); err != nil {
				t.Fatalf("age the published receipts: %v", err)
			}
		},
		terminal: func(t *testing.T, eventID uuid.UUID) []audit.Event {
			t.Helper()
			rows, err := admin.Query(t.Context(), readOutboxEventsStatement, eventID)
			if err != nil {
				t.Fatalf("read the YugabyteDB outbox: %v", err)
			}
			defer rows.Close()
			var events []audit.Event
			for rows.Next() {
				var event audit.Event
				if err := rows.Scan(&event); err != nil {
					t.Fatalf("scan the YugabyteDB outbox: %v", err)
				}
				events = append(events, event)
			}
			return events
		},
		childEnv: func() []string {
			return []string{readChildBackendVariable + "=yugabytedb", readChildDSNVariable + "=" + dsn}
		},
	}
}

// applicationRolePool opens a pool that runs every statement as app_auth.
func applicationRolePool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse the ledger address: %v", err)
	}
	config.ConnConfig.RuntimeParams["role"] = "app_auth"
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatalf("open the application role pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
