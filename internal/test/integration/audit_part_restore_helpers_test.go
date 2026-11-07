package integration_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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
	payload := []byte(partRestorePayloadPrefix + uuid.NewString())
	digest := sha256.Sum256(payload)
	root := hex.EncodeToString(digest[:])
	fixture := partRestoreRawFixture{
		admin: admin, org: uuid.Must(uuid.NewV7()), target: uuid.Must(uuid.NewV7()), source: uuid.Must(uuid.NewV7()), payload: payload,
	}
	targetHash, sourceHash := randomBytes(t, partRestoreHashBytes), randomBytes(t, partRestoreHashBytes)
	fixture.insertEvent(t, fixture.target, root, partRestoreTargetSeq, targetHash)
	fixture.insertEvent(t, fixture.source, root, partRestoreSourceSeq, sourceHash)
	mustExec(t, admin, readTestSQL(t, "part_restore_part_insert.sql"), fixture.org, fixture.source, root, payload)
	fixture.call = partRestoreCall{
		org: fixture.org, event: fixture.target, source: fixture.source, root: root,
		rowHash: targetHash, seq: partRestoreTargetSeq, shard: partRestoreFixtureShard,
		sourceRowHash: sourceHash, sourceSeq: partRestoreSourceSeq, sourceShard: partRestoreFixtureShard,
		changeID: uuid.Must(uuid.NewV7()), header: []byte(`{"synthetic":"change event"}`), parts: [][]byte{},
	}
	t.Cleanup(func() { cleanupPartRestoreOutbox(t, admin, fixture.call.changeID) })
	return fixture
}

func cleanupPartRestoreOutbox(t *testing.T, admin *pgxpool.Pool, changeID uuid.UUID) {
	t.Helper()
	_, _ = admin.Exec(context.WithoutCancel(t.Context()), `DELETE FROM public.ops_outbox WHERE event_id = $1`, changeID)
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
