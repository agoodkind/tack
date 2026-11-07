package integration_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	partRestoreIdentityCount = 25
	partRestoreShardMask     = 255
	partRestoreAppendPause   = 20 * time.Millisecond
	partRestoreAppendVerb    = "runtime.audit.test"
	partRestoreQueryLimit    = 1000
)

type partRestorePipeline struct {
	ledger       *nodeAuditLedger
	org          uuid.UUID
	target       uuid.UUID
	source       uuid.UUID
	manifest     ops.AuditPartRestoreManifest
	manifestPath string
	fileDigest   string
	repairDir    string
	operatorID   uuid.UUID
	operatorDSN  string
	lifecycle    *pgxpool.Pool
	operatorPool *pgxpool.Pool
}

type partRestoreRun struct {
	stdout string
	stderr string
	err    error
}

func partRestoreTargets() []audit.Target {
	targets := make([]audit.Target, 0, partRestoreIdentityCount)
	targets = append(targets, readNodeTarget())
	for len(targets) < partRestoreIdentityCount {
		targets = append(targets, audit.Target{
			Kind: audit.TargetKindNode, NodeType: "issue", Reference: "", Name: "", URI: "",
			RelationType: "", ID: uuid.Must(uuid.NewV7()), SourceID: uuid.Nil, TargetID: uuid.Nil,
		})
	}
	return targets
}

func storeOnePartRead(t *testing.T, ledger *nodeAuditLedger, org uuid.UUID, targets []audit.Target) uuid.UUID {
	t.Helper()
	root, parts, err := audit.ReadManifestDigest(targets)
	if err != nil || parts != 1 {
		t.Fatalf("read manifest digest = %q, %d parts, error %v, want one part", root, parts, err)
	}
	fingerprint := sha256.Sum256([]byte(uuid.NewString()))
	read := completeEvent(org, userActor(), targets[0], audit.OperationRead, audit.PhaseRead, audit.OutcomeCompleted)
	read.Context.Read = &audit.ReadEvidence{
		Count: partRestoreIdentityCount, PayloadSHA256: hex.EncodeToString(fingerprint[:]), PayloadFormat: "application/json",
		ManifestSHA256: root, ManifestParts: parts,
	}
	encoded, err := audit.EncodeEvent(read, targets)
	if err != nil {
		t.Fatalf("encode read event %s: %v", read.EventID, err)
	}
	transaction, err := ledger.admin.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin the outbox write of %s: %v", read.EventID, err)
	}
	defer func() { _ = transaction.Rollback(t.Context()) }()
	if err := audit.WriteEncodedOutboxTx(t.Context(), transaction, encoded); err != nil {
		t.Fatalf("write read event %s to the outbox: %v", read.EventID, err)
	}
	if err := transaction.Commit(t.Context()); err != nil {
		t.Fatalf("commit the outbox write of %s: %v", read.EventID, err)
	}
	waitForOnePartRead(t, ledger, org, read.EventID, root)
	return read.EventID
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

func startPartRestorePipeline(t *testing.T) *partRestorePipeline {
	t.Helper()
	ledger := startNodeAuditLedger(t)
	org := uuid.Must(uuid.NewV7())
	targets := partRestoreTargets()
	targetID := storeOnePartRead(t, ledger, org, targets)
	sourceID := storeOnePartRead(t, ledger, org, targets)
	targetRow, err := ledger.reader.GetByID(t.Context(), targetID)
	if err != nil {
		t.Fatalf("read target %s: %v", targetID, err)
	}
	sourceRow, err := ledger.reader.GetByID(t.Context(), sourceID)
	if err != nil {
		t.Fatalf("read source %s: %v", sourceID, err)
	}
	root := targetRow.Context.Read.ManifestSHA256
	if _, err := ledger.admin.Exec(t.Context(), readTestSQL(t, "manifest_test_delete_part.sql"), org, targetID); err != nil {
		t.Fatalf("delete the target part: %v", err)
	}
	manifest := ops.AuditPartRestoreManifest{
		SchemaVersion: 1, Ticket: "TACK-620", OrgID: org, TargetEventID: targetID, SourceEventID: sourceID, PartIndex: 0,
		ManifestSHA256: root, PartDigest: root, DeclaredParts: 1, IdentityCount: partRestoreIdentityCount,
		TargetSeq: targetRow.Seq, TargetShard: targetRow.Shard, TargetHashVersion: targetRow.HashVersion, TargetRowHash: targetRow.RowHash,
		SourceSeq: sourceRow.Seq, SourceShard: sourceRow.Shard, SourceRowHash: sourceRow.RowHash,
	}
	pipeline := &partRestorePipeline{ledger: ledger, org: org, target: targetID, source: sourceID, manifest: manifest, operatorID: uuid.Must(uuid.NewV7())}
	pipeline.repairDir = t.TempDir()
	pipeline.writeManifest(t, manifest)
	ledgerDSN := testenv.Ledger(t)
	pipeline.operatorDSN = partRestoreLoginDSN(t, ledger.admin, ledgerDSN, "audit_operator")
	pipeline.lifecycle = rolePool(t, ledgerDSN, "audit_operator")
	pipeline.operatorPool = rolePool(t, ledgerDSN, "audit_operator")
	return pipeline
}

func (p *partRestorePipeline) writeManifest(t *testing.T, manifest ops.AuditPartRestoreManifest) {
	t.Helper()
	content, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode the manifest: %v", err)
	}
	digest := sha256.Sum256(content)
	p.manifestPath = filepath.Join(p.repairDir, "manifest-"+uuid.NewString()+".json")
	if err := os.WriteFile(p.manifestPath, content, 0o600); err != nil {
		t.Fatalf("write the manifest: %v", err)
	}
	p.fileDigest = hex.EncodeToString(digest[:])
}

func (p *partRestorePipeline) run(t *testing.T, execute bool, extra ...string) partRestoreRun {
	t.Helper()
	cfg := *p.ledger.cfg
	cfg.RepairDir, cfg.AuditOperatorDSN = p.repairDir, p.operatorDSN
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	factory := cli.System(&cfg)
	factory.Out, factory.Err = stdout, stderr
	factory.SetAuditOutbox(audit.NewPoolOutbox(p.ledger.admin))
	factory.SetAuditReads(audit.NewPoolReadLifecycle(p.lifecycle))
	root := nodeAuditCommandRoot(factory)
	root.SetContext(integration.CommandContext(t))
	arguments := []string{
		"--output", "json", "--operator-id", p.operatorID.String(), "--operator-email", "part-restore@example.test",
		"ops", "audit", "backfill", "once-restore-manifest-part", "--manifest", p.manifestPath, "--manifest-sha256", p.fileDigest,
	}
	if execute {
		arguments = append([]string{"--execute"}, arguments...)
	}
	root.SetArgs(slices.Concat(arguments, extra))
	err := root.Execute()
	return partRestoreRun{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func (r partRestoreRun) status(t *testing.T) string {
	t.Helper()
	var envelope struct {
		Result struct {
			Status string `json:"status"`
			Plan   string `json:"plan"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &envelope); err != nil {
		t.Fatalf("decode the command output %q: %v", r.stdout, err)
	}
	return envelope.Result.Status + " " + envelope.Result.Plan
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

type ledgerRowSnapshot struct {
	seq         int64
	prevHash    []byte
	rowHash     []byte
	hashVersion int16
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
