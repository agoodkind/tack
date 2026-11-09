package integration_test

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/ops"
)

const (
	partRestoreSQLState      = "TK620"
	partRestoreFixtureShard  = 7
	partRestoreTargetSeq     = 5
	partRestoreSourceSeq     = 9
	partRestoreHashBytes     = 32
	partRestorePayloadPrefix = "synthetic manifest payload "
)

type partRestoreCall struct {
	org, event, source     uuid.UUID
	root                   string
	rowHash, sourceRowHash []byte
	seq, sourceSeq         int64
	shard, sourceShard     int16
	changeID               uuid.UUID
	header                 []byte
	parts                  [][]byte
}

type partRestoreRawFixture struct {
	admin   *pgxpool.Pool
	org     uuid.UUID
	target  uuid.UUID
	source  uuid.UUID
	payload []byte
	call    partRestoreCall
}

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		t.Fatalf("read random bytes: %v", err)
	}
	return buffer
}

func mustExec(t *testing.T, pool *pgxpool.Pool, statement string, arguments ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), statement, arguments...); err != nil {
		t.Fatalf("run %q: %v", statement, err)
	}
}

func (f partRestoreRawFixture) insertEvent(t *testing.T, event uuid.UUID, root string, seq int64, hash []byte) {
	t.Helper()
	mustExec(t, f.admin, readTestSQL(t, "part_restore_event_insert.sql"),
		f.org, int16(partRestoreFixtureShard), event, seq, uuid.Must(uuid.NewV7()), root, randomBytes(t, partRestoreHashBytes), hash)
}

func newPartRestoreRawFixture(t *testing.T, admin *pgxpool.Pool) partRestoreRawFixture {
	t.Helper()
	return newPartRestoreRawFixtureWithPayload(t, admin, []byte(partRestorePayloadPrefix+uuid.NewString()))
}

func buildRestoreChange(t *testing.T, manifest ops.AuditPartRestoreManifest, fileDigest string, payload []byte, actorID uuid.UUID) (uuid.UUID, []byte, [][]byte) {
	t.Helper()
	return buildRestoreChangeWithTool(t, manifest, fileDigest, payload, actorID, "")
}

func buildRestoreChangeWithTool(t *testing.T, manifest ops.AuditPartRestoreManifest, fileDigest string, payload []byte, actorID uuid.UUID, tool string) (uuid.UUID, []byte, [][]byte) {
	t.Helper()
	actor := audit.Actor{
		Type: audit.ActorOperator, ID: actorID, Email: "", Name: "", SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
	}

	operation, err := audit.NewMutationOperation(actor, audit.SourceSystem, tool)
	if err != nil {
		t.Fatalf("build the operation: %v", err)
	}
	operation.Context.RequestID, operation.Context.TraceID = uuid.NewString(), audit.SHA256Hex([]byte(uuid.NewString()))[:32]
	mutationContext, err := audit.WithMutationContext(t.Context(), operation)
	if err != nil {
		t.Fatalf("attach the operation: %v", err)
	}
	encoded, err := ops.BuildAuditPartChange(mutationContext, manifest, fileDigest, payload)
	if err != nil {
		t.Fatalf("build the change event with the production builder: %v", err)
	}
	header, parts, err := encoded.HeaderAndParts()
	if err != nil {
		t.Fatalf("split the change event: %v", err)
	}

	return encoded.EventID, header, parts
}

func newPartRestoreRawFixtureWithPayload(t *testing.T, admin *pgxpool.Pool, payload []byte) partRestoreRawFixture {
	t.Helper()

	root := audit.SHA256Hex(payload)
	fixture := partRestoreRawFixture{
		admin: admin, org: uuid.Must(uuid.NewV7()), target: uuid.Must(uuid.NewV7()), source: uuid.Must(uuid.NewV7()), payload: payload,
	}
	targetHash, sourceHash := randomBytes(t, partRestoreHashBytes), randomBytes(t, partRestoreHashBytes)
	fixture.insertEvent(t, fixture.target, root, partRestoreTargetSeq, targetHash)
	fixture.insertEvent(t, fixture.source, root, partRestoreSourceSeq, sourceHash)
	mustExec(t, admin, readTestSQL(t, "part_restore_part_insert.sql"), fixture.org, fixture.source, root, payload)
	manifest := ops.AuditPartRestoreManifest{
		SchemaVersion: 1, Ticket: "TACK-620", OrgID: fixture.org, TargetEventID: fixture.target, SourceEventID: fixture.source,
		PartIndex: 0, ManifestSHA256: root, DeclaredParts: 1, IdentityCount: partRestoreIdentityCount,
		TargetSeq: partRestoreTargetSeq, TargetShard: partRestoreFixtureShard, TargetHashVersion: 1, TargetRowHash: targetHash,
		SourceSeq: partRestoreSourceSeq, SourceShard: partRestoreFixtureShard, SourceRowHash: sourceHash,
	}
	changeID, header, parts := buildRestoreChange(t, manifest, audit.SHA256Hex([]byte("fixture manifest file")), payload, uuid.Must(uuid.NewV7()))
	fixture.call = partRestoreCall{
		org: fixture.org, event: fixture.target, source: fixture.source, root: root,
		rowHash: targetHash, seq: partRestoreTargetSeq, shard: partRestoreFixtureShard,
		sourceRowHash: sourceHash, sourceSeq: partRestoreSourceSeq, sourceShard: partRestoreFixtureShard,
		changeID: changeID, header: header, parts: parts,
	}

	t.Cleanup(func() { cleanupPartRestoreOutbox(t, admin, fixture.call.changeID) })
	return fixture
}

func cleanupPartRestoreOutbox(t *testing.T, admin *pgxpool.Pool, changeID uuid.UUID) {
	t.Helper()

	if _, err := admin.Exec(context.WithoutCancel(t.Context()), readTestSQL(t, "part_restore_outbox_delete.sql"), changeID); err != nil {
		t.Errorf("delete the fixture outbox row: %v", err)
	}
}

func (c partRestoreCall) run(t *testing.T, pool *pgxpool.Pool) error {
	t.Helper()
	_, err := pool.Exec(t.Context(), readTestSQL(t, "part_restore_call.sql"),
		c.org, c.event, c.source, c.root, c.rowHash, c.seq, c.shard,
		c.sourceRowHash, c.sourceSeq, c.sourceShard, c.changeID, c.header, c.parts)
	return err
}

func requirePartRestoreRefusal(t *testing.T, err error, fragment string) {
	t.Helper()

	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != partRestoreSQLState || !strings.Contains(postgresError.Message, fragment) {
		t.Fatalf("restore error = %v, want SQLSTATE %s containing %q", err, partRestoreSQLState, fragment)
	}
}

func requirePGCode(t *testing.T, err error, code string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != code {
		t.Fatalf("error = %v, want SQLSTATE %s", err, code)
	}
}

type partRestoreCounts struct {
	events, heads, projected, pii, parts, outbox int64
}

func readPartRestoreCounts(t *testing.T, admin *pgxpool.Pool, changeID uuid.UUID) partRestoreCounts {
	t.Helper()
	var counts partRestoreCounts
	err := admin.QueryRow(t.Context(), readTestSQL(t, "part_restore_counts.sql"), changeID).Scan(
		&counts.events, &counts.heads, &counts.projected, &counts.pii, &counts.parts, &counts.outbox)
	if err != nil {
		t.Fatalf("count the ledger rows: %v", err)
	}
	return counts
}
