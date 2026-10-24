package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// legacyJobWriteTimeout bounds the FoundationDB transaction that writes the
// legacy job record.
const legacyJobWriteTimeout = 5 * time.Second

// legacyTool, legacyRequestID, and legacyTraceID are the tool name, request ID,
// and trace ID that the legacy audit template records.
const (
	legacyTool      = "tack_delete_issue"
	legacyRequestID = "legacy-request"
	legacyTraceID   = "legacy-trace"
)

// legacyJobRecord encodes a subtree delete job record in the format that the
// current main release writes. It stores actor_id and audit_template and does
// not store operation.
type legacyJobRecord struct {
	ID            uuid.UUID       `json:"id"`
	OrgID         uuid.UUID       `json:"org_id"`
	RootID        uuid.UUID       `json:"root_id"`
	RootParentID  uuid.UUID       `json:"root_parent_id"`
	ActorID       uuid.UUID       `json:"actor_id"`
	AuditTemplate json.RawMessage `json:"audit_template,omitempty"`
	Stack         []uuid.UUID     `json:"stack"`
	Deleted       int             `json:"deleted"`
	Moved         int             `json:"moved"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// TestRuntimeAuditLegacyJobRecovery checks audit events after each subtest writes
// a running job record in the current main release format for an issue with a
// child issue and a comment. Each subtest finishes the job with
// [goodkind.io/tack/internal/service.NodeService.ResumeSubtreeDeletes] under a
// different test operation.
//
// The audit_template subtest requires every change event to record the template
// actor, source, tool, request ID, and trace ID, and one shared operation ID. The
// actor_id subtest omits the audit template and requires every change event to
// record the job's actor_id, the system source ([audit.SourceSystem]), and one
// shared operation ID. Both subtests require that operation ID to differ from
// the resume call's operation ID.
func TestRuntimeAuditLegacyJobRecovery(t *testing.T) {
	t.Run("audit_template", func(t *testing.T) {
		env := integration.SetupTestEnv(t)
		templateActor := uuid.New()
		events, operationID := resumeLegacyJob(t, env, templateActor, true)
		for _, event := range events {
			requireChangeEvent(t, event, operationID, templateActor)
			eventContext := event.Context
			if eventContext.Source != audit.SourceMCP || eventContext.Tool != legacyTool ||
				eventContext.RequestID != legacyRequestID || eventContext.TraceID != legacyTraceID {
				t.Fatalf("event %s records source %s, tool %q, request ID %q, and trace ID %q but must record the audit template's values", event.EventID, eventContext.Source, eventContext.Tool,
					eventContext.RequestID, eventContext.TraceID)
			}
		}
	})
	t.Run("actor_id", func(t *testing.T) {
		env := integration.SetupTestEnv(t)
		jobActor := uuid.New()
		events, operationID := resumeLegacyJob(t, env, jobActor, false)
		for _, event := range events {
			requireChangeEvent(t, event, operationID, jobActor)
			if event.Context.Source != audit.SourceSystem {
				t.Fatalf("event %s records source %s but must record source system", event.EventID, event.Context.Source)
			}
		}
	})
}

// resumeLegacyJob creates an issue with a child issue and a comment, then writes
// a running legacy job record for the issue with actor_id set to actorID. When
// templated is true, it includes an audit template with actorID as the actor.
//
// It runs [goodkind.io/tack/internal/service.NodeService.ResumeSubtreeDeletes]
// and requires one finished job and one node.delete event each for the issue
// and the comment. It requires the first event's operation ID to differ from
// the operation ID in env.Ctx. It returns the change events after the job
// started and the first event's operation ID.
func resumeLegacyJob(t *testing.T, env *integration.TestEnv, actorID uuid.UUID, templated bool) ([]audit.Event, uuid.UUID) {
	t.Helper()
	projectID := createSchemaProject(t, env)
	root := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	createSchemaNode(t, env, root.ID, projectID, "issue", nil)
	comment := createSchemaNode(t, env, root.ID, projectID, "comment", schemaProps("description", quoted("Legacy comment")))
	mark := outboxTail(t, env)

	record := legacyJobRecord{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, RootID: root.ID, RootParentID: projectID, ActorID: actorID,
		AuditTemplate: nil, Stack: []uuid.UUID{root.ID}, Deleted: 0, Moved: 0, UpdatedAt: clock.Now().Add(-time.Hour).UTC(),
	}
	if templated {
		record.AuditTemplate = legacyTemplate(t, env, actorID, root.ID)
	}
	writeLegacyJob(t, record)
	if finished, err := env.NodeSvc.ResumeSubtreeDeletes(env.Ctx, 0, env.LedgerResumeAudit()); err != nil || finished != 1 {
		t.Fatalf("ResumeSubtreeDeletes finished %d jobs with error %v instead of finishing one job without an error", finished, err)
	}

	events := outboxEventsAfter(t, env, mark)
	requireOneEvent(t, events, audit.VerbNodeDelete, root.ID)
	requireOneEvent(t, events, audit.VerbNodeDelete, comment.ID)
	operationID := events[0].Context.OperationID
	if operationID == operationIDOf(t, env.Ctx) {
		t.Fatalf("change events use the resume call's operation %s instead of the operation that LegacyJobOperation derives from the job", operationID)
	}
	return events, operationID
}

// legacyTemplate returns a format 0 node.delete event for rootID with a user
// actor actorID, the mcp source ([audit.SourceMCP]), legacyTool, legacyRequestID,
// and legacyTraceID. It uses the shape that the current main release stores as
// the audit template.
func legacyTemplate(t *testing.T, env *integration.TestEnv, actorID, rootID uuid.UUID) json.RawMessage {
	t.Helper()
	template, err := audit.MarshalEvent(audit.Event{
		Verb: string(audit.VerbNodeDelete), EventID: uuid.Must(uuid.NewV7()),
		Actor: audit.Actor{
			Type: audit.ActorUser, ID: actorID, Email: "legacy@example.test", Name: "", SessionID: "",
			IP: "", UserAgent: "", RequestID: legacyRequestID, APITokenLabel: "",
		},
		Entity: audit.Entity{Type: "node", NodeType: "issue", ID: rootID, Identifier: "", Name: ""},
		Context: audit.EventContext{
			OrgID: env.OrgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: legacyRequestID, TraceID: legacyTraceID, Source: audit.SourceMCP, Tool: legacyTool, RPC: "", Reason: "",
		},
		Delta: nil, Outcome: audit.OutcomeOK, Error: nil, IdempotencyKey: "", OccurredAt: clock.Now().UTC(), Extra: nil,
	})
	if err != nil {
		t.Fatalf("the test could not encode the legacy audit template: %v", err)
	}
	return template
}

// writeLegacyJob stores record under the subtree delete job key in the active
// test key prefix.
func writeLegacyJob(t *testing.T, record legacyJobRecord) {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("the test could not encode legacy job record %s: %v", record.ID, err)
	}
	database, err := fdbadapter.Open(testenv.FoundationDB(t), legacyJobWriteTimeout)
	if err != nil {
		t.Fatalf("the test could not open the FoundationDB database for the legacy job record: %v", err)
	}
	key := fdb.Key(append(append([]byte{}, fdbadapter.TestPrefixRange()...), tuple.Tuple{"node_delete_job", record.ID.String()}.Pack()...))
	if _, err := database.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(key, encoded)
		return nil, nil
	}); err != nil {
		t.Fatalf("the test could not store legacy job record %s: %v", record.ID, err)
	}
}
