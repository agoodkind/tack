package integration_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

func (p *partRestorePipeline) partCount(t *testing.T) int {
	t.Helper()
	parts, err := p.ledger.reader.ManifestPartDigests(t.Context(), p.org, p.target)
	if err != nil {
		t.Fatalf("read the part digests of %s: %v", p.target, err)
	}
	return len(parts)
}

func (p *partRestorePipeline) queryRows(t *testing.T, org uuid.UUID, verb audit.Verb) []audit.Row {
	t.Helper()
	rows, err := p.ledger.reader.Query(t.Context(), audit.QueryFilter{
		OrgID: org, Oldest: time.Unix(0, 0).UTC(), Latest: clock.Now().Add(time.Hour), Action: string(verb),
		ActorID: p.operatorID, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: partRestoreQueryLimit,
	})
	if err != nil {
		t.Fatalf("query %s rows: %v", verb, err)
	}
	return rows
}

func (p *partRestorePipeline) waitRows(t *testing.T, org uuid.UUID, verb audit.Verb, ready func([]audit.Row) bool) []audit.Row {
	t.Helper()
	deadline := clock.Now().Add(nodeAuditDeadline)

	for {
		rows := p.queryRows(t, org, verb)
		if ready(rows) {
			return rows
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger has %d %s rows after %s, not the expected rows", len(rows), verb, nodeAuditDeadline)
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
	slices.SortFunc(selected, func(left, right audit.Row) int { return left.Context.StartedAt.Compare(right.Context.StartedAt) })
	return selected
}

func (p *partRestorePipeline) requireOperationRecords(t *testing.T, operations int) (intent, result audit.Row) {
	t.Helper()
	rows := p.waitRows(t, audit.SystemOrgID(), audit.VerbOpsBackfillRestoreManifestPart, func(rows []audit.Row) bool {
		return len(operationRows(rows, audit.PhaseIntent)) >= operations && len(operationRows(rows, audit.PhaseResult)) >= operations
	})
	intents, results := operationRows(rows, audit.PhaseIntent), operationRows(rows, audit.PhaseResult)
	if len(intents) != operations || len(results) != operations {
		t.Fatalf("operation rows = %d intents and %d results, want %d of each", len(intents), len(results), operations)
	}

	intent, result = intents[operations-1], results[operations-1]
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
			Manifest       string `json:"manifest"`
			ManifestSHA256 string `json:"manifest_sha256"`
			TargetEventID  string `json:"target_event_id"`
		} `json:"input"`
	}
	if err := json.Unmarshal(intent.Extra, &extra); err != nil {
		t.Fatalf("decode the intent extra %s: %v", intent.Extra, err)
	}

	if extra.Input.Manifest != p.manifestPath || extra.Input.ManifestSHA256 != p.fileDigest || extra.Input.TargetEventID != p.target.String() {
		t.Fatalf("intent input = %+v, want the manifest path %s, digest %s, and target %s", extra.Input, p.manifestPath, p.fileDigest, p.target)
	}
}

func (p *partRestorePipeline) requireStepRecords(t *testing.T, intent audit.Row) {
	t.Helper()

	isStep := func(row audit.Row) bool { return row.Context.ParentOperationID == intent.Context.OperationID }

	rows := p.waitRows(t, audit.SystemOrgID(), audit.VerbOpsBackfillRestoreManifestPart, func(rows []audit.Row) bool {
		return len(slices.DeleteFunc(slices.Clone(rows), func(row audit.Row) bool { return !isStep(row) })) >= 2
	})
	for _, row := range rows {
		if !isStep(row) {
			continue
		}

		var identity struct {
			OrgID         uuid.UUID `json:"org_id"`
			TargetEventID uuid.UUID `json:"target_event_id"`
			SourceEventID uuid.UUID `json:"source_event_id"`
			PartIndex     int64     `json:"part_index"`
			RootDigest    string    `json:"root_digest"`
		}
		manifest := p.manifest
		if row.Context.Target == nil || row.Context.Target.URI != p.partURI() || row.Context.Target.NodeType != audit.TargetAuditEventPart ||
			json.Unmarshal(row.Extra, &identity) != nil || identity.OrgID != manifest.OrgID || identity.TargetEventID != manifest.TargetEventID ||
			identity.SourceEventID != manifest.SourceEventID || identity.PartIndex != manifest.PartIndex || identity.RootDigest != manifest.ManifestSHA256 {
			t.Fatalf("step row %s = target %+v, extra %s, want the part target and the manifest identities", row.EventID, row.Context.Target, row.Extra)
		}
		if row.Context.Phase == audit.PhaseResult {
			p.requireObservedEvidence(t, row)
		}
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

	rows := p.waitRows(t, p.org, audit.VerbAuditEventPartCreate, func(rows []audit.Row) bool { return len(rows) >= changes })
	if len(rows) != changes {
		t.Fatalf("ledger has %d %s rows, want %d", len(rows), audit.VerbAuditEventPartCreate, changes)
	}
	change, eventContext := rows[0], rows[0].Context
	if eventContext.OperationClass != audit.OperationDatabaseChange || eventContext.Phase != audit.PhaseChange ||
		eventContext.AuditFormatVersion != audit.AuditFormatComplete || change.Outcome != audit.OutcomeCompleted ||
		eventContext.Target == nil || eventContext.Target.URI != p.partURI() || eventContext.Target.NodeType != audit.TargetAuditEventPart ||
		eventContext.Target.Kind != audit.TargetKindResource || change.ActorID != p.operatorID || change.OrgID != p.org ||
		eventContext.OperationID != intent.Context.OperationID {
		t.Fatalf("change row = %+v, want a complete change of %s by %s in operation %s", eventContext, p.partURI(), p.operatorID, intent.Context.OperationID)
	}
	if change.Delta == nil || len(change.Delta.Before) != 0 {
		t.Fatalf("change delta = %+v, want no Before value", change.Delta)
	}
	var after struct {
		OrgID     uuid.UUID `json:"org_id"`
		EventID   uuid.UUID `json:"event_id"`
		PartIndex int64     `json:"part_index"`
		Digest    string    `json:"digest"`
		Payload   struct {
			PIIRef uuid.UUID `json:"pii_ref"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(change.Delta.After, &after); err != nil || after.OrgID != p.org || after.EventID != p.target ||
		after.PartIndex != 0 || after.Digest != p.manifest.ManifestSHA256 || after.Payload.PIIRef == uuid.Nil {
		t.Fatalf("change After = %s, error %v, want the restored part identity, digest, and payload reference", change.Delta.After, err)
	}
	var provenance struct {
		SourceEventID      uuid.UUID `json:"source_event_id"`
		ManifestFileSHA256 string    `json:"manifest_file_sha256"`
	}
	if err := json.Unmarshal(change.Extra, &provenance); err != nil || provenance.SourceEventID != p.source || provenance.ManifestFileSHA256 != p.fileDigest {
		t.Fatalf("change provenance = %s, error %v, want source %s and manifest digest %s", change.Extra, err, p.source, p.fileDigest)
	}

	p.requireProtectedPayload(t, after.Payload.PIIRef)

	return change
}
