package integration_test

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
)

const (
	partRestoreAppendsPerPhase = 3
	partRestoreRepairedStatus  = "restored "
	partRestorePresentStatus   = "already_present "
	partRestorePlannedMissing  = "planned part_missing"
)

func TestAuditPartRestoreCommandRepairsMissingPart(t *testing.T) {
	pipeline := startPartRestorePipeline(t)
	appender := newShardAppender(pipeline, pipeline.manifest.TargetShard)
	appender.appendCount(t, partRestoreAppendsPerPhase)

	repro := runManifestParts(
		t,
		pipeline.ledger,
		manifestRunOptions{
			readerDSN:  pipeline.ledger.cfg.AuditReaderDSN,
			lifecycle:  pipeline.lifecycle,
			operatorID: pipeline.operatorID,
		},
		manifestArgs(
			pipeline.org.String(),
			pipeline.target.String(),
			"--verify-payloads",
			"--execute",
		)...,
	)
	if repro.err == nil || !strings.Contains(repro.err.Error(), "0 of 1 read manifest parts") {
		t.Fatalf("verification before the repair = %+v, want a 0 of 1 parts failure", repro)
	}

	if err := pipeline.exportVerdict(t); err == nil {
		t.Fatal(
			"the export of the org verified before the repair, want a failure for the missing part",
		)
	}

	appender.requireAllStored(t)
	before := pipeline.snapshot(t)

	dry := pipeline.run(t, false)
	if dry.err != nil || pipeline.partCount(t) != 0 || dry.status(t) != partRestorePlannedMissing {
		t.Fatalf(
			"dry run = %+v, parts %d, want the plan %q and no part",
			dry,
			pipeline.partCount(t),
			partRestorePlannedMissing,
		)
	}

	stop := appender.during(t)
	run := pipeline.run(t, true)

	stop()
	if run.err != nil || run.status(t) != partRestoreRepairedStatus {
		t.Fatalf("restore run = %+v, want status restored", run)
	}

	if during := len(appender.appended) - partRestoreAppendsPerPhase; during < 1 {
		t.Fatalf("appends during the restore = %d, want at least 1", during)
	}

	appender.appendCount(t, partRestoreAppendsPerPhase)
	appender.requireAllStored(t)

	verification, err := pipeline.ledger.reader.VerifyManifest(
		t.Context(),
		pipeline.org,
		pipeline.target,
	)
	if err != nil || !verification.PayloadVerified ||
		verification.IdentityCount != partRestoreIdentityCount ||
		verification.PartCount != 1 ||
		verification.DeclaredDigest != verification.RecomputedDigest {
		t.Fatalf(
			"verification after the repair = %+v, %v, want 25 of 25 identities in 1 of 1 parts",
			verification,
			err,
		)
	}

	requireSnapshotUnchanged(t, before, pipeline.snapshot(t))
	pipeline.ledger.requireChainVerifies(t, pipeline.org)
	if err := pipeline.exportVerdict(t); err != nil {
		t.Fatalf("export verdict after the repair = %v, want a clean chain", err)
	}

	requestID, traceID := integration.CommandCorrelation(t, run.stdout)
	pipeline.requireNoPayloadText(t, run.stdout+run.stderr)
	intent, result := pipeline.requireOperationRecords(t, 1)
	pipeline.requireNoPayloadText(t, string(intent.Extra)+string(result.Extra))
	pipeline.requireStepRecords(t, intent)
	change := pipeline.requireChangeRecord(t, intent, 1)
	requireRowsCorrelation(t, requestID, traceID, intent, result, change)

	again := pipeline.run(t, true)
	if again.err != nil || again.status(t) != partRestorePresentStatus {
		t.Fatalf("rerun = %+v, want status already_present", again)
	}

	againRequestID, againTraceID := integration.CommandCorrelation(t, again.stdout)
	if againRequestID == requestID || againTraceID == traceID {
		t.Fatalf("rerun reused request ID %s or trace ID %s", againRequestID, againTraceID)
	}

	pipeline.requireChangeRecord(t, intent, 1)
	intent, result = pipeline.requireOperationRecords(t, 2)
	requireRowsCorrelation(t, againRequestID, againTraceID, intent, result)
}

func TestAuditPartRestoreRerunAfterUnseenCommit(t *testing.T) {
	pipeline := startPartRestorePipeline(t)
	changeID, header, parts := buildRestoreChange(
		t,
		pipeline.manifest,
		pipeline.fileDigest,
		pipeline.payload,
		pipeline.operatorID,
	)
	manifest := pipeline.manifest

	call := partRestoreCall{
		org:           manifest.OrgID,
		event:         manifest.TargetEventID,
		source:        manifest.SourceEventID,
		root:          manifest.ManifestSHA256,
		rowHash:       manifest.TargetRowHash,
		seq:           manifest.TargetSeq,
		shard:         manifest.TargetShard,
		sourceRowHash: manifest.SourceRowHash,
		sourceSeq:     manifest.SourceSeq,
		sourceShard:   manifest.SourceShard,
		changeID:      changeID,
		header:        header,
		parts:         parts,
	}
	if err := call.run(t, pipeline.operatorPool); err != nil {
		t.Fatalf("restore call = %v, want no error", err)
	}

	pipeline.waitRows(
		t,
		pipeline.org,
		audit.VerbAuditEventPartCreate,
		func(rows []audit.Row) bool { return len(rows) >= 1 },
	)

	run := pipeline.run(t, true)
	if run.err != nil || run.status(t) != partRestorePresentStatus {
		t.Fatalf("rerun after an unseen commit = %+v, want status already_present", run)
	}

	pipeline.requireOperationRecords(t, 1)
	pipeline.ledger.requireChainVerifies(t, pipeline.org)
}
