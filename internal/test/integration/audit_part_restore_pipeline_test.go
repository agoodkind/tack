package integration_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
)

const (
	partRestoreIdentityCount = 25
	partRestoreQueryLimit    = 1000
)

type partRestorePipeline struct {
	ledger       *nodeAuditLedger
	org          uuid.UUID
	target       uuid.UUID
	source       uuid.UUID
	payload      []byte
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
	fingerprint := audit.SHA256Hex([]byte(uuid.NewString()))
	read := completeEvent(org, userActor(), targets[0], audit.OperationRead, audit.PhaseRead, audit.OutcomeCompleted)
	read.Context.Read = &audit.ReadEvidence{
		Count: partRestoreIdentityCount, PayloadSHA256: fingerprint, PayloadFormat: "application/json",
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
	sourceParts, err := ledger.reader.ManifestParts(t.Context(), org, sourceID)
	if err != nil || len(sourceParts) != 1 {
		t.Fatalf("read the source part: %d parts, %v", len(sourceParts), err)
	}
	mustExec(t, ledger.admin, readTestSQL(t, "manifest_test_delete_part.sql"), org, targetID)
	root := targetRow.Context.Read.ManifestSHA256
	manifest := ops.AuditPartRestoreManifest{
		SchemaVersion: 1, Ticket: "TACK-620", OrgID: org, TargetEventID: targetID, SourceEventID: sourceID, PartIndex: 0,
		ManifestSHA256: root, DeclaredParts: 1, IdentityCount: partRestoreIdentityCount,
		TargetSeq: targetRow.Seq, TargetShard: targetRow.Shard, TargetHashVersion: targetRow.HashVersion, TargetRowHash: targetRow.RowHash,
		SourceSeq: sourceRow.Seq, SourceShard: sourceRow.Shard, SourceRowHash: sourceRow.RowHash,
	}
	pipeline := &partRestorePipeline{
		ledger: ledger, org: org, target: targetID, source: sourceID, payload: sourceParts[0].Payload,
		manifest: manifest, operatorID: uuid.Must(uuid.NewV7()), repairDir: t.TempDir(),
	}
	pipeline.writeManifest(t, manifest)
	ledgerDSN := testenv.Ledger(t)
	pipeline.operatorDSN = roleLoginDSN(t, ledger.admin, ledgerDSN, "audit_operator")
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
	p.manifestPath = filepath.Join(p.repairDir, "manifest-"+uuid.NewString()+".json")
	if err := os.WriteFile(p.manifestPath, content, 0o600); err != nil {
		t.Fatalf("write the manifest: %v", err)
	}
	p.fileDigest = audit.SHA256Hex(content)
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
	root.SetContext(testenv.CommandContext(t))
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

func (p *partRestorePipeline) partURI() string {
	return "tack:audit-event/" + p.target.String() + "/part/0"
}
