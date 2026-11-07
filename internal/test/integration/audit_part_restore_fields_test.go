package integration_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

const otherToken = "{other}"

type partRestoreMutation struct {
	name     string
	fragment string
	edit     func(t *testing.T, encoding *changeEncoding, other string)
}

type partRestoreWrites struct {
	parts, outbox int64
}

func headerSet(name, fragment, path, value string) partRestoreMutation {
	return partRestoreMutation{name: name, fragment: fragment, edit: func(t *testing.T, e *changeEncoding, other string) {
		t.Helper()
		e.editHeader(t, path, json.RawMessage(strings.ReplaceAll(value, otherToken, other)))
	}}
}

func headerDrop(name, fragment, path string) partRestoreMutation {
	return partRestoreMutation{name: name, fragment: fragment, edit: func(t *testing.T, e *changeEncoding, _ string) {
		t.Helper()
		e.editHeader(t, path, nil)
	}}
}

func (p *partRestorePipeline) builtChange(t *testing.T, tool string) (uuid.UUID, changeEncoding) {
	t.Helper()
	changeID, header, parts := buildRestoreChangeWithTool(t, p.manifest, p.fileDigest, p.payload, p.operatorID, tool)
	encoding := changeEncoding{header: header, parts: nil}
	for _, part := range parts {
		encoding.parts = append(encoding.parts, part)
	}
	return changeID, encoding
}

func (p *partRestorePipeline) restoreCall(changeID uuid.UUID, encoding *changeEncoding) partRestoreCall {
	manifest := p.manifest
	return partRestoreCall{
		org: manifest.OrgID, event: manifest.TargetEventID, source: manifest.SourceEventID, root: manifest.ManifestSHA256,
		rowHash: manifest.TargetRowHash, seq: manifest.TargetSeq, shard: manifest.TargetShard,
		sourceRowHash: manifest.SourceRowHash, sourceSeq: manifest.SourceSeq, sourceShard: manifest.SourceShard,
		changeID: changeID, header: encoding.headerBytes(), parts: encoding.partBytes(),
	}
}

func (p *partRestorePipeline) writes(t *testing.T, changeID uuid.UUID) partRestoreWrites {
	t.Helper()
	var counts partRestoreWrites
	if err := p.ledger.admin.QueryRow(t.Context(), readTestSQL(t, "part_restore_writes.sql"), p.org, p.target, changeID).Scan(&counts.parts, &counts.outbox); err != nil {
		t.Fatalf("count the part and outbox rows: %v", err)
	}
	return counts
}

func (p *partRestorePipeline) requireRefusedChange(t *testing.T, tool string, mutation partRestoreMutation) {
	t.Helper()
	changeID, encoding := p.builtChange(t, tool)
	mutation.edit(t, &encoding, uuid.NewString())
	call := p.restoreCall(changeID, &encoding)
	requirePartRestoreRefusal(t, call.run(t, p.operatorPool), mutation.fragment)
	if after := p.writes(t, changeID); after != (partRestoreWrites{parts: 0, outbox: 0}) {
		t.Fatalf("rows after the refusal = %+v, want no part row and no outbox row", after)
	}
}

func (p *partRestorePipeline) requireAcceptedChange(t *testing.T, tool string) {
	t.Helper()
	changeID, encoding := p.builtChange(t, tool)
	call := p.restoreCall(changeID, &encoding)
	if err := call.run(t, p.operatorPool); err != nil {
		t.Fatalf("restore call = %v, want no error", err)
	}
	rows := p.waitRows(t, p.org, audit.VerbAuditEventPartCreate, func(rows []audit.Row) bool { return len(rows) >= 1 })
	if len(rows) != 1 || rows[0].EventID != changeID {
		t.Fatalf("ledger change rows = %d, first %v, want only change event %s", len(rows), rows[0].EventID, changeID)
	}
	if p.partCount(t) != 1 {
		t.Fatalf("restored part rows = %d, want 1", p.partCount(t))
	}
	p.ledger.requireChainVerifies(t, p.org)
}

func changeEventMutations() []partRestoreMutation {
	const (
		idFragment      = "change event ID is not the derived change ID"
		formatFragment  = "not a format 2 event"
		phaseFragment   = "not a database change in the change phase"
		outcomeFragment = "completed outcome without an error"
		timeFragment    = "valid occurrence, start, or completion time"
		targetFragment  = "target is not part 0 of the restored event"
		afterFragment   = "after record does not name the restored part and manifest digest"
		protectedFrag   = "one protected payload reference for the restored event"
		unknownFragment = "field in "
		actorFragment   = "known actor kind and an actor ID"
		correlationFrag = "lacks a request ID or a trace ID"
		entityFragment  = "entity differs from its target"
		otherUUID       = `"{other}"`
	)
	otherPayload := `"` + base64.StdEncoding.EncodeToString([]byte("another payload")) + `"`
	return []partRestoreMutation{
		headerSet("event ID", idFragment, "event_id", otherUUID),
		headerSet("verb", "verb is not audit.event_part_create", "verb", `"audit.event_part_delete"`),
		headerSet("organization", "organization is not the restored organization", "context.org_id", otherUUID),
		headerDrop("occurrence time omitted", timeFragment, "occurred_at"),
		headerSet("occurrence time not RFC 3339", timeFragment, "occurred_at", `"yesterday"`),
		headerSet("zero occurrence time", timeFragment, "occurred_at", `"0001-01-01T00:00:00Z"`),
		headerDrop("start time omitted", timeFragment, "context.started_at"),
		headerDrop("completion time omitted", timeFragment, "context.completed_at"),
		headerSet("completion before start", "completion time precedes its start time", "context.completed_at", `"2000-01-01T00:00:00Z"`),
		headerSet("format version", formatFragment, "context.audit_format_version", "1"),
		headerSet("operation class", phaseFragment, "context.operation_class", `"external"`),
		headerSet("phase", phaseFragment, "context.phase", `"result"`),
		headerSet("outcome", outcomeFragment, "outcome", `"failed"`),
		headerSet("error", outcomeFragment, "error", `{"code":"failed","message":"failed"}`),
		headerSet("committed steps", outcomeFragment, "context.committed_steps", `["step"]`),
		headerSet("read evidence", "includes read evidence", "context.read", `{"payload_format":"application/json"}`),
		headerDrop("operation ID omitted", "lacks an operation ID", "context.operation_id"),
		headerDrop("request ID omitted", correlationFrag, "context.request_id"),
		headerDrop("trace ID omitted", correlationFrag, "context.trace_id"),
		headerSet("actor ID nil", actorFragment, "actor.id", `"00000000-0000-0000-0000-000000000000"`),
		headerSet("actor kind", actorFragment, "actor.type", `"unauthenticated"`),
		headerSet("target URI", targetFragment, "context.target.uri", `"tack:audit-event/{other}/part/0"`),
		headerSet("target kind", targetFragment, "context.target.kind", `"node"`),
		headerSet("target node type", targetFragment, "context.target.node_type", `"issue"`),
		headerSet("target ID", targetFragment, "context.target.id", otherUUID),
		headerSet("target owner", targetFragment, "context.target.owner_id", otherUUID),
		headerSet("entity kind", entityFragment, "entity.type", `"node"`),
		headerSet("entity ID", entityFragment, "entity.id", otherUUID),
		headerSet("before value", "includes a before value", "delta.before", `{"digest":"previous"}`),
		headerSet("after organization", afterFragment, "delta.after.org_id", otherUUID),
		headerSet("after event ID", afterFragment, "delta.after.event_id", otherUUID),
		headerSet("after part index", afterFragment, "delta.after.part_index", "1"),
		headerSet("after digest", afterFragment, "delta.after.digest", `"`+strings.Repeat("0", 64)+`"`),
		headerSet("payload reference omitted", protectedFrag, "delta.after.payload", "{}"),
		headerDrop("protected values omitted", protectedFrag, "protected"),
		headerSet("protected reference", protectedFrag, "protected.0.ref", otherUUID),
		headerSet("protected subject", protectedFrag, "protected.0.subject_id", otherUUID),
		headerSet("protected value type", protectedFrag, "protected.0.value", "123"),
		headerSet("protected value", "protected payload of the change event differs", "protected.0.value", otherPayload),
		headerSet("source event", "does not name the source event", "extra.source_event_id", otherUUID),
		headerSet("unknown event field", unknownFragment+"event", "surprise", "true"),
		headerSet("unknown context field", unknownFragment+"context", "context.surprise", "true"),
		headerSet("synthetic header", unknownFragment+"event", "synthetic", `"change event"`),
	}
}

func TestAuditPartRestoreRefusesEveryChangeEventField(t *testing.T) {
	pipeline := startPartRestorePipeline(t)
	for _, current := range changeEventMutations() {
		t.Run(current.name, func(t *testing.T) {
			pipeline.requireRefusedChange(t, "", current)
		})
	}
	t.Run("valid change event is projected into the ledger", func(t *testing.T) {
		pipeline.requireAcceptedChange(t, "")
	})
}
