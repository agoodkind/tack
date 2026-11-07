package integration_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
)

const (
	partRestoreAppendsPerPhase = 3
	partRestoreRepairedStatus  = "restored "
	partRestorePresentStatus   = "already_present "
	partRestorePlannedMissing  = "planned part_missing"
)

func (p *partRestorePipeline) partURI() string {
	return "tack:audit-event/" + p.target.String() + "/part/0"
}

func (p *partRestorePipeline) snapshotParts(t *testing.T) int {
	t.Helper()
	parts, err := p.ledger.reader.ManifestPartDigests(t.Context(), p.org, p.target)
	if err != nil {
		t.Fatalf("read the part digests of %s: %v", p.target, err)
	}
	return len(parts)
}

func (p *partRestorePipeline) rowsByVerb(t *testing.T, org uuid.UUID, verb audit.Verb, want int) []audit.Row {
	t.Helper()
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		rows, err := p.ledger.reader.Query(t.Context(), audit.QueryFilter{
			OrgID: org, Oldest: time.Unix(0, 0).UTC(), Latest: clock.Now().Add(time.Hour), Action: string(verb),
			ActorID: p.operatorID, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: partRestoreQueryLimit,
		})
		if err != nil {
			t.Fatalf("query %s rows: %v", verb, err)
		}
		if len(rows) >= want {
			return rows
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger has %d %s rows after %s, want %d", len(rows), verb, nodeAuditDeadline, want)
		}
		time.Sleep(nodeAuditPollInterval)
	}
}

func operationRows(rows []audit.Row, phase audit.Phase) []audit.Row {
	selected := []audit.Row{}
	for _, row := range rows {
		if row.Context.Phase == phase && row.Context.ParentOperationID == uuid.Nil {
			selected = append(selected, row)
		}
	}
	return selected
}

func (p *partRestorePipeline) requireOperationRecords(t *testing.T, operations int) (intent, result audit.Row) {
	t.Helper()
	var intents, results []audit.Row
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		rows := p.rowsByVerb(t, audit.SystemOrgID(), audit.VerbOpsBackfillRestoreManifestPart, operations*2)
		intents, results = operationRows(rows, audit.PhaseIntent), operationRows(rows, audit.PhaseResult)
		if len(intents) >= operations && len(results) >= operations {
			break
		}
		if clock.Now().After(deadline) {
			t.Fatalf("operation rows = %d intents and %d results, want %d of each", len(intents), len(results), operations)
		}
		time.Sleep(nodeAuditPollInterval)
	}
	if len(intents) != operations || len(results) != operations {
		t.Fatalf("operation rows = %d intents and %d results, want %d of each", len(intents), len(results), operations)
	}
	slices.SortFunc(intents, func(left, right audit.Row) int { return left.Context.StartedAt.Compare(right.Context.StartedAt) })
	slices.SortFunc(results, func(left, right audit.Row) int { return left.Context.StartedAt.Compare(right.Context.StartedAt) })
	intent, result = intents[0], results[0]
	for _, row := range []audit.Row{intent, result} {
		if row.Context.OperationClass != audit.OperationExternal || row.Context.AuditFormatVersion != audit.AuditFormatComplete ||
			row.ActorID != p.operatorID || row.Context.Target == nil || row.Context.Target.URI != p.partURI() ||
			row.Context.Target.Kind != audit.TargetKindResource || row.Context.Target.NodeType != audit.TargetAuditEventPart {
			t.Fatalf("operation row %s = class %q, actor %s, target %+v", row.EventID, row.Context.OperationClass, row.ActorID, row.Context.Target)
		}
	}
	if intent.Outcome != audit.OutcomePending || intent.Context.StartedAt.IsZero() || !intent.Context.CompletedAt.IsZero() {
		t.Fatalf("intent row = outcome %q, started %v, completed %v", intent.Outcome, intent.Context.StartedAt, intent.Context.CompletedAt)
	}
	if result.Outcome != audit.OutcomeCompleted || result.Context.OperationID != intent.Context.OperationID || result.Context.CompletedAt.IsZero() {
		t.Fatalf("result row = outcome %q, operation %s, completed %v, want completed for %s", result.Outcome, result.Context.OperationID, result.Context.CompletedAt, intent.Context.OperationID)
	}
	p.requireSafeInput(t, intent)
	return intent, result
}

func (p *partRestorePipeline) requireSafeInput(t *testing.T, intent audit.Row) {
	t.Helper()
	var extra struct {
		Input struct {
			ManifestFileSHA256 string `json:"manifest_file_sha256"`
			OrgID              string `json:"org_id"`
			TargetEventID      string `json:"target_event_id"`
			SourceEventID      string `json:"source_event_id"`
			PartIndex          int64  `json:"part_index"`
			RootDigest         string `json:"root_digest"`
		} `json:"input"`
	}
	if err := json.Unmarshal(intent.Extra, &extra); err != nil {
		t.Fatalf("decode the intent extra %s: %v", intent.Extra, err)
	}
	input := extra.Input
	if input.ManifestFileSHA256 != p.fileDigest || input.OrgID != p.org.String() || input.TargetEventID != p.target.String() ||
		input.SourceEventID != p.source.String() || input.PartIndex != 0 || input.RootDigest != p.manifest.ManifestSHA256 {
		t.Fatalf("intent input = %+v, want the manifest identities and digests", input)
	}
	parts, err := p.ledger.reader.ManifestParts(t.Context(), p.org, p.source)
	if err != nil || len(parts) != 1 {
		t.Fatalf("read the source part: %d parts, %v", len(parts), err)
	}
	if bytes.Contains(intent.Extra, parts[0].Payload) {
		t.Fatal("the intent extra contains payload bytes")
	}
}

func requireRowsCorrelation(t *testing.T, requestID, traceID string, rows ...audit.Row) {
	t.Helper()
	for _, row := range rows {
		if row.Context.RequestID == "" || row.Context.TraceID == "" || row.Context.RequestID != requestID || row.Context.TraceID != traceID {
			t.Fatalf("row %s %s stores request ID %q and trace ID %q, want the command _meta request ID %q and trace ID %q",
				row.EventID, row.Action, row.Context.RequestID, row.Context.TraceID, requestID, traceID)
		}
	}
}

func (p *partRestorePipeline) requireChangeRecord(t *testing.T, intent audit.Row, changes int) audit.Row {
	t.Helper()
	rows := p.rowsByVerb(t, p.org, audit.VerbAuditEventPartCreate, changes)
	if len(rows) != changes {
		t.Fatalf("ledger has %d %s rows, want %d", len(rows), audit.VerbAuditEventPartCreate, changes)
	}
	change := rows[0]
	eventContext := change.Context
	if eventContext.OperationClass != audit.OperationDatabaseChange || eventContext.Phase != audit.PhaseChange ||
		eventContext.AuditFormatVersion != audit.AuditFormatComplete || change.Outcome != audit.OutcomeCompleted ||
		eventContext.Target == nil || eventContext.Target.URI != p.partURI() || eventContext.Target.NodeType != audit.TargetAuditEventPart ||
		eventContext.Target.Kind != audit.TargetKindResource || change.ActorID != p.operatorID || change.OrgID != p.org ||
		eventContext.OperationID != intent.Context.OperationID {
		t.Fatalf("change row = %+v, want a complete change of %s by %s in operation %s", eventContext, p.partURI(), p.operatorID, intent.Context.OperationID)
	}
	if eventContext.RequestID != intent.Context.RequestID || eventContext.TraceID != intent.Context.TraceID {
		t.Fatalf("change request and trace IDs = %q and %q, want %q and %q from the command intent",
			eventContext.RequestID, eventContext.TraceID, intent.Context.RequestID, intent.Context.TraceID)
	}
	if change.Delta == nil || len(change.Delta.Before) != 0 {
		t.Fatalf("change delta = %+v, want no Before value", change.Delta)
	}
	var after struct {
		OrgID     string `json:"org_id"`
		EventID   string `json:"event_id"`
		PartIndex int64  `json:"part_index"`
		Digest    string `json:"digest"`
	}
	if err := json.Unmarshal(change.Delta.After, &after); err != nil || after.OrgID != p.org.String() || after.EventID != p.target.String() ||
		after.PartIndex != 0 || after.Digest != p.manifest.PartDigest {
		t.Fatalf("change After = %s, error %v, want the restored part identity and digest", change.Delta.After, err)
	}
	var provenance struct {
		SourceEventID      string `json:"source_event_id"`
		ManifestFileSHA256 string `json:"manifest_file_sha256"`
	}
	if err := json.Unmarshal(change.Extra, &provenance); err != nil || provenance.SourceEventID != p.source.String() || provenance.ManifestFileSHA256 != p.fileDigest {
		t.Fatalf("change provenance = %s, error %v, want source %s and manifest digest %s", change.Extra, err, p.source, p.fileDigest)
	}
	return change
}

func TestAuditPartRestoreCommandRepairsMissingPart(t *testing.T) {
	pipeline := startPartRestorePipeline(t)
	appender := newShardAppender(pipeline, pipeline.manifest.TargetShard)
	appender.appendCount(t, partRestoreAppendsPerPhase)

	repro := runManifestParts(t, pipeline.ledger, manifestRunOptions{
		readerDSN: pipeline.ledger.cfg.AuditReaderDSN, lifecycle: pipeline.lifecycle, operatorID: pipeline.operatorID,
	}, manifestArgs(pipeline.org.String(), pipeline.target.String(), "--verify-payloads", "--execute")...)
	if repro.err == nil || !strings.Contains(repro.err.Error(), "0 of 1 read manifest parts") {
		t.Fatalf("verification before the repair = %+v, want a 0 of 1 parts failure", repro)
	}
	if err := pipeline.exportVerdict(t); err == nil {
		t.Fatal("the export of the org verified before the repair, want a failure for the missing part")
	}
	appender.requireAllStored(t)
	before := pipeline.snapshot(t)

	dry := pipeline.run(t, false)
	if dry.err != nil || pipeline.snapshotParts(t) != 0 || dry.status(t) != partRestorePlannedMissing {
		t.Fatalf("dry run = %+v, parts %d, want the plan %q and no part", dry, pipeline.snapshotParts(t), partRestorePlannedMissing)
	}

	stop := appender.during(t)
	run := pipeline.run(t, true)
	stop()
	if run.err != nil || run.status(t) != partRestoreRepairedStatus {
		t.Fatalf("restore run = %+v, want status restored", run)
	}
	duringCount := len(appender.appended) - partRestoreAppendsPerPhase
	if duringCount < 1 {
		t.Fatalf("appends during the restore = %d, want at least 1", duringCount)
	}
	appender.appendCount(t, partRestoreAppendsPerPhase)
	appender.requireAllStored(t)

	verification, err := pipeline.ledger.reader.VerifyManifest(t.Context(), pipeline.org, pipeline.target)
	if err != nil || !verification.PayloadVerified || verification.IdentityCount != partRestoreIdentityCount ||
		verification.PartCount != 1 || verification.DeclaredDigest != verification.RecomputedDigest {
		t.Fatalf("verification after the repair = %+v, %v, want 25 of 25 identities in 1 of 1 parts", verification, err)
	}
	requireSnapshotUnchanged(t, before, pipeline.snapshot(t))
	pipeline.ledger.requireChainVerifies(t, pipeline.org)
	if err := pipeline.exportVerdict(t); err != nil {
		t.Fatalf("export verdict after the repair = %v, want a clean chain", err)
	}

	requestID, traceID := integration.CommandCorrelation(t, run.stdout)
	intent, result := pipeline.requireOperationRecords(t, 1)
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
	intent, result = pipeline.requireOperationRecords(t, 2)
	change = pipeline.requireChangeRecord(t, intent, 1)
	requireRowsCorrelation(t, requestID, traceID, intent, result, change)
}

func TestAuditPartRestoreRerunAfterUnseenCommit(t *testing.T) {
	pipeline := startPartRestorePipeline(t)
	actor := audit.Actor{
		Type: audit.ActorOperator, ID: pipeline.operatorID, Email: "", Name: "", SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
	}
	operation, err := audit.NewMutationOperation(actor, audit.SourceSystem, "")
	if err != nil {
		t.Fatalf("build the operation: %v", err)
	}
	operation.Context.OrgID = pipeline.org
	after, err := json.Marshal(struct {
		OrgID     uuid.UUID `json:"org_id"`
		EventID   uuid.UUID `json:"event_id"`
		PartIndex int64     `json:"part_index"`
		Digest    string    `json:"digest"`
	}{OrgID: pipeline.org, EventID: pipeline.target, PartIndex: 0, Digest: pipeline.manifest.PartDigest})
	if err != nil {
		t.Fatalf("encode the part record: %v", err)
	}
	target := audit.Target{
		Kind: audit.TargetKindResource, NodeType: audit.TargetAuditEventPart, Reference: "", Name: "", URI: pipeline.partURI(),
		RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	change, err := audit.NewChange(uuid.Must(uuid.NewV7()), target, nil, after)
	if err != nil {
		t.Fatalf("build the change: %v", err)
	}
	event, err := audit.ChangeEvent(operation, change, clock.Now())
	if err != nil {
		t.Fatalf("build the change event for a resource target without a Before value: %v", err)
	}
	encoded, err := audit.EncodeEvent(event, nil)
	if err != nil {
		t.Fatalf("encode the change event: %v", err)
	}
	header, parts, err := encoded.HeaderAndParts()
	if err != nil {
		t.Fatalf("split the change event: %v", err)
	}
	call := partRestoreCall{
		org: pipeline.org, event: pipeline.target, source: pipeline.source, partIndex: 0, root: pipeline.manifest.ManifestSHA256,
		rowHash: pipeline.manifest.TargetRowHash, seq: pipeline.manifest.TargetSeq, shard: pipeline.manifest.TargetShard,
		sourceRowHash: pipeline.manifest.SourceRowHash, sourceSeq: pipeline.manifest.SourceSeq, sourceShard: pipeline.manifest.SourceShard,
		changeID: encoded.EventID, header: header, parts: parts,
	}
	if inserted, err := call.run(t, pipeline.operatorPool); err != nil || inserted != 1 {
		t.Fatalf("restore call = %d, %v, want 1 and no error", inserted, err)
	}
	pipeline.rowsByVerb(t, pipeline.org, audit.VerbAuditEventPartCreate, 1)

	run := pipeline.run(t, true)
	if run.err != nil || run.status(t) != partRestorePresentStatus {
		t.Fatalf("rerun after an unseen commit = %+v, want status already_present", run)
	}
	pipeline.requireOperationRecords(t, 1)
	pipeline.ledger.requireChainVerifies(t, pipeline.org)
}
