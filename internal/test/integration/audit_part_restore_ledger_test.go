package integration_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"hash/crc32"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

const (
	partRestoreShardMask   = 255
	partRestoreAppendPause = 20 * time.Millisecond
	partRestoreAppendVerb  = "runtime.audit.test"
)

type ledgerRowSnapshot struct {
	seq         int64
	prevHash    []byte
	rowHash     []byte
	hashVersion int16
}

func waitForOnePartRead(t *testing.T, ledger *nodeAuditLedger, org, eventID uuid.UUID, root string) {
	t.Helper()
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		_, rowErr := ledger.reader.GetByID(t.Context(), eventID)
		parts, partsErr := ledger.reader.ManifestParts(t.Context(), org, eventID)
		if rowErr == nil && partsErr == nil && len(parts) == 1 {
			if parts[0].Digest != root {
				t.Fatalf("single part digest = %s, want the manifest root %s", parts[0].Digest, root)
			}
			return
		}
		if rowErr != nil && !errors.Is(rowErr, pgx.ErrNoRows) {
			t.Fatalf("read event %s: %v", eventID, rowErr)
		}
		if clock.Now().After(deadline) {
			t.Fatalf("event %s stored %d parts after %s", eventID, len(parts), nodeAuditDeadline)
		}
		time.Sleep(nodeAuditPollInterval)
	}
}

func (p *partRestorePipeline) exportVerdict(t *testing.T) error {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate the export key: %v", err)
	}
	directory := t.TempDir()
	_, err = audit.Export(t.Context(), p.ledger.reader, private, "ed25519:part-restore-test", audit.QueryFilter{
		OrgID: p.org, Oldest: time.Unix(0, 0).UTC(), Latest: clock.Now().Add(time.Hour),
		Action: "", ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 0,
	}, directory)
	if err != nil {
		return err
	}
	report, err := audit.VerifyBundle(directory, public)
	if err != nil {
		return err
	}
	return report.Err()
}

func (p *partRestorePipeline) snapshot(t *testing.T) map[uuid.UUID]ledgerRowSnapshot {
	t.Helper()
	rows, err := p.ledger.admin.Query(t.Context(), readTestSQL(t, "part_restore_ledger_rows.sql"), p.org)
	if err != nil {
		t.Fatalf("snapshot the ledger rows: %v", err)
	}
	defer rows.Close()
	snapshot := map[uuid.UUID]ledgerRowSnapshot{}
	for rows.Next() {
		var eventID uuid.UUID
		var row ledgerRowSnapshot
		if err := rows.Scan(&eventID, &row.seq, &row.prevHash, &row.rowHash, &row.hashVersion); err != nil {
			t.Fatalf("scan a ledger row: %v", err)
		}
		snapshot[eventID] = row
	}
	if rows.Err() != nil {
		t.Fatalf("read the ledger rows: %v", rows.Err())
	}
	return snapshot
}

func requireSnapshotUnchanged(t *testing.T, before, after map[uuid.UUID]ledgerRowSnapshot) {
	t.Helper()
	for eventID, was := range before {
		now, ok := after[eventID]
		if !ok || was.seq != now.seq || was.hashVersion != now.hashVersion || !bytes.Equal(was.prevHash, now.prevHash) || !bytes.Equal(was.rowHash, now.rowHash) {
			t.Fatalf("ledger row %s changed: before %+v, after %+v (present %v)", eventID, was, now, ok)
		}
	}
}

type shardAppender struct {
	pipeline *partRestorePipeline
	outbox   *audit.PoolOutbox
	actor    audit.Actor
	shard    int16
	mutex    sync.Mutex
	appended []uuid.UUID
}

func newShardAppender(p *partRestorePipeline, shard int16) *shardAppender {
	return &shardAppender{pipeline: p, outbox: audit.NewPoolOutbox(p.ledger.admin), actor: userActor(), shard: shard}
}

func (a *shardAppender) appendOne() error {
	for {
		eventID := uuid.Must(uuid.NewV7())
		var buffer [32]byte
		copy(buffer[0:16], a.actor.ID[:])
		copy(buffer[16:32], eventID[:])
		if int16(crc32.ChecksumIEEE(buffer[:])&partRestoreShardMask) != a.shard {
			continue
		}
		event := completeEvent(a.pipeline.org, a.actor, syntheticExternalTarget(), audit.OperationExternal, audit.PhaseIntent, audit.OutcomePending)
		event.EventID, event.Context.CompletedAt = eventID, time.Time{}
		if err := a.outbox.WriteOutbox(context.Background(), event); err != nil {
			return err
		}
		a.mutex.Lock()
		a.appended = append(a.appended, eventID)
		a.mutex.Unlock()
		return nil
	}
}

func (a *shardAppender) appendCount(t *testing.T, count int) {
	t.Helper()
	for range count {
		if err := a.appendOne(); err != nil {
			t.Fatalf("append to shard %d: %v", a.shard, err)
		}
	}
}

func (a *shardAppender) during(t *testing.T) func() {
	t.Helper()
	done := make(chan struct{})
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			if err := a.appendOne(); err != nil {
				t.Errorf("append to shard %d during the restore: %v", a.shard, err)
				return
			}
			time.Sleep(partRestoreAppendPause)
		}
	}()
	return func() {
		close(done)
		group.Wait()
	}
}

func (a *shardAppender) requireAllStored(t *testing.T) int {
	t.Helper()
	a.mutex.Lock()
	want := len(a.appended)
	a.mutex.Unlock()
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		rows, err := a.pipeline.ledger.reader.Query(t.Context(), audit.QueryFilter{
			OrgID: a.pipeline.org, Oldest: time.Unix(0, 0).UTC(), Latest: clock.Now().Add(time.Hour), Action: partRestoreAppendVerb,
			ActorID: a.actor.ID, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: partRestoreQueryLimit,
		})
		if err != nil {
			t.Fatalf("query the appended events: %v", err)
		}
		if len(rows) == want {
			return want
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger stored %d of %d appended events after %s", len(rows), want, nodeAuditDeadline)
		}
		time.Sleep(nodeAuditPollInterval)
	}
}
