package integration_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

const (
	// storedBundleRows is the number of rows in the stored export.
	storedBundleRows = 4
	// ownerFillerBytes makes a change event larger than one event part.
	ownerFillerBytes = audit.EventPartMaxBytes + 1024
)

// ownerRecord is the new value of a change with one protected member.
type ownerRecord struct {
	Text   audit.ProtectedReference `json:"text"`
	Filler string                   `json:"filler"`
}

// TestRuntimeAuditStoredRowsWithoutOwner verifies the export that revision
// ee06d5ad wrote before Target declared OwnerID. Its rows are a format 0 read,
// a format 2 resource change with a protected reference, a relationship
// change, and a read with a manifest. Each row must pass offline verification
// and must encode to its stored bytes.
func TestRuntimeAuditStoredRowsWithoutOwner(t *testing.T) {
	directory := filepath.Join("testdata", "legacy_audit", "ee06d5ad_bundle")
	keyText, err := os.ReadFile(filepath.Join(directory, "signer_public_key.hex"))
	if err != nil {
		t.Fatalf("read the signer public key: %v", err)
	}

	public, err := hex.DecodeString(strings.TrimSpace(string(keyText)))
	if err != nil {
		t.Fatalf("decode the signer public key: %v", err)
	}

	report, err := audit.VerifyBundle(directory, ed25519.PublicKey(public))
	if err != nil {
		t.Fatalf("verify the stored bundle: %v", err)
	}

	if report.Err() != nil || report.RowsScanned != storedBundleRows ||
		report.HashMatches != storedBundleRows {
		t.Fatalf(
			"stored bundle report = %+v, err = %v, want %d verified rows",
			report,
			report.Err(),
			storedBundleRows,
		)
	}

	names, err := filepath.Glob(filepath.Join(directory, "events-*.jsonl"))
	if err != nil || len(names) != 1 {
		t.Fatalf("stored bundle rows files = %v, %v, want one file", names, err)
	}

	stored, err := os.ReadFile(names[0])
	if err != nil {
		t.Fatalf("read the stored rows: %v", err)
	}

	for line := range bytes.SplitSeq(bytes.TrimSuffix(stored, []byte("\n")), []byte("\n")) {
		var row audit.Row
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatalf("decode a stored row: %v", err)
		}

		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("encode stored row %s: %v", row.EventID, err)
		}

		if !bytes.Equal(encoded, line) {
			t.Fatalf("stored row %s encodes to %s, want %s", row.EventID, encoded, line)
		}
	}
}

// TestRuntimeAuditOwnerRoundTrip writes a single-record change and a multipart
// change with OwnerID to the FoundationDB outbox through AppendChanges. The real
// relay and consumer must store both rows with the owner, the protected values,
// and a row hash that ComputeRowHash reproduces, and the export must pass
// verification. A multipart event with a value under Target.ID must leave an
// invalid assembly without a ledger row or a dead letter.
func TestRuntimeAuditOwnerRoundTrip(t *testing.T) {
	clusterFile := testenv.FoundationDB(t)
	identity := uuid.New()
	prefix := append([]byte("tack-test:"), identity[:]...)
	fdbadapter.SetTestPrefix(prefix)
	t.Cleanup(func() { clearReadPrefix(t, clusterFile, prefix) })
	database, err := fdbadapter.Open(clusterFile, nodeAuditTransactionTimeout)
	if err != nil {
		t.Fatalf("open FoundationDB: %v", err)
	}

	oldest := clock.Now().UTC().Add(-time.Minute)
	orgID, ownerID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	operation := completeEvent(
		orgID,
		userActor(),
		ownedTarget(uuid.Must(uuid.NewV7()), uuid.Nil),
		audit.OperationDatabaseChange,
		audit.PhaseResult,
		audit.OutcomeCompleted,
	)
	filler := strings.Repeat("x", ownerFillerBytes)
	plainText, largeText := "plain owner value "+uuid.NewString(), "large owner value "+uuid.NewString()
	plain, plainReference := ownerChange(t, ownerID, plainText, "")
	large, largeReference := ownerChange(t, ownerID, largeText, filler)
	refused, _ := ownerChange(t, ownerID, "refused owner value "+uuid.NewString(), filler)
	refusedEvent, err := audit.ChangeEvent(operation, refused, clock.Now())
	if err != nil {
		t.Fatalf("build the change event %s: %v", refused.EventID, err)
	}

	refusedEvent.Protected[0].SubjectID = refused.Target.ID
	refusedRecords, err := audit.EncodeEvent(refusedEvent, nil)
	if err != nil || !refusedRecords.Multipart() {
		t.Fatalf("encode the refused event: multipart %t, %v", refusedRecords.Multipart(), err)
	}

	if _, err := database.Transact(func(tr fdb.Transaction) (any, error) {
		return nil, fdbadapter.AppendChanges(
			t.Context(),
			tr,
			operation,
			[]audit.Change{plain, large},
		)
	}); err != nil {
		t.Fatalf("append the owner changes: %v", err)
	}

	if _, err := database.Transact(func(tr fdb.Transaction) (any, error) {
		return nil, fdbadapter.NewOutboxWriter(tr).WriteEncodedEvent(t.Context(), refusedRecords)
	}); err != nil {
		t.Fatalf("write the refused event: %v", err)
	}

	pipeline := newPartsPipeline(t)
	pipeline.startConsumer(t)
	startLegacyRelay(t, pipeline, clusterFile)

	for _, change := range []audit.Change{plain, large} {
		row := pipeline.stack().waitForRow(t, change.EventID)
		if !reflect.DeepEqual(*row.Context.Target, change.Target) {
			t.Fatalf(
				"event %s target = %+v, want %+v",
				change.EventID,
				*row.Context.Target,
				change.Target,
			)
		}

		hash, err := audit.ComputeRowHash(row)
		if err != nil || !bytes.Equal(hash, row.RowHash) {
			t.Fatalf(
				"ComputeRowHash of event %s = %x, %v, want %x",
				change.EventID,
				hash,
				err,
				row.RowHash,
			)
		}
	}

	if pipeline.count(t, completedRowsQuery, plain.EventID) != 0 ||
		pipeline.count(t, completedRowsQuery, large.EventID) != 1 ||
		pipeline.count(t, assemblyRowsQuery, large.EventID) != 0 {
		t.Fatalf(
			"events %s and %s did not use the plain and multipart paths",
			plain.EventID,
			large.EventID,
		)
	}

	requireProtectedValues(
		t,
		pipeline.admin,
		map[uuid.UUID]string{plainReference: plainText, largeReference: largeText},
	)
	waitFor(
		t,
		"rejection of the assembly",
		func() bool {
			return pipeline.count(t, invalidAssemblyRowsQuery, refused.EventID) == 1
		},
	)
	if _, err := pipeline.reader.GetByID(t.Context(), refused.EventID); !errors.Is(
		err,
		pgx.ErrNoRows,
	) {
		t.Fatalf("ledger read of refused event %s = %v, want pgx.ErrNoRows", refused.EventID, err)
	}

	pipeline.stack().waitForDeadLetters(t, 0)
	if err := pipeline.stack().exportAndVerify(
		t,
		orgID,
		oldest,
		clock.Now().UTC().Add(time.Minute),
	); err != nil {
		t.Fatalf("export of events with an owner failed verification: %v", err)
	}
}

// ownerChange returns a change on a new owned target with one protected member
// that stores text under ownerID. Filler sets the size of the change.
func ownerChange(t *testing.T, ownerID uuid.UUID, text, filler string) (audit.Change, uuid.UUID) {
	t.Helper()
	reference := uuid.Must(uuid.NewV7())
	after, err := json.Marshal(ownerRecord{
		Text:   audit.ProtectedReference{PIIRef: reference},
		Filler: filler,
	})
	if err != nil {
		t.Fatalf("encode the record: %v", err)
	}

	value, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("encode the protected value: %v", err)
	}

	change, err := audit.NewChange(
		uuid.Must(uuid.NewV7()),
		ownedTarget(uuid.Must(uuid.NewV7()), ownerID),
		nil,
		after,
	)
	if err != nil {
		t.Fatalf("build the change: %v", err)
	}

	change.Protected = []audit.ProtectedValue{{Ref: reference, SubjectID: ownerID, Value: value}}

	return change, reference
}
