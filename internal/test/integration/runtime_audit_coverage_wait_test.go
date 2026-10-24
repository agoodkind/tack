//go:build runtimeaudit

package integration_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// coverageLateTargetDelay is the time between the ledger row of the child
// node event and the recording of the target node event.
const coverageLateTargetDelay = 2 * time.Second

// TestRuntimeAuditCoverageWaitsForTargetEvent records one operation that
// deletes a child node and a target node. The ledger stores the child node
// event first, and the test records the target node event after
// coverageLateTargetDelay. waitForRows must return rows that contain the
// target node event.
func TestRuntimeAuditCoverageWaitsForTargetEvent(t *testing.T) {
	stack := startRuntimeAuditStack(t)
	env := &coverageEnv{
		server: nil, ledger: publicLedger{stack: stack, since: clock.Now().Add(-time.Hour)}, admin: nil,
		commandClasses: nil, nodes: nil, searchEnabled: false, backup: nil,
	}
	requestID := newRequestID(t, "coverage-wait")
	operation, err := audit.NewMutationOperation(audit.Actor{
		Type: audit.ActorUser, ID: uuid.Must(uuid.NewV7()), Email: "", Name: "", SessionID: "",
		IP: "", UserAgent: "", RequestID: requestID, APITokenLabel: "",
	}, audit.SourceMCP, "tack_delete_project")
	if err != nil {
		t.Fatalf("Creating the delete operation failed: %v", err)
	}
	operation.Context.OrgID, operation.Context.RequestID = uuid.Must(uuid.NewV7()), requestID
	child := coverageDeleteEvent(t, operation)
	target := coverageDeleteEvent(t, operation)
	stack.record(t, child)
	stack.waitForRow(t, child.EventID)
	recorded := make(chan error, 1)
	go func() {
		time.Sleep(coverageLateTargetDelay)
		recorded <- stack.recorder.Record(context.WithoutCancel(t.Context()), target)
	}()
	evidence := coverageEvidence{
		orgID: operation.Context.OrgID, requestID: requestID, actorID: operation.Actor.ID, outcome: "", outbox: nil,
		body: nil, targetID: target.Context.Target.ID,
	}
	rows := env.waitForRows(t, evidence, audit.VerbNodeDelete, audit.OutcomeCompleted)
	if err := <-recorded; err != nil {
		t.Fatalf("Recording the target node event %s failed: %v", target.EventID, err)
	}
	if !slices.ContainsFunc(rows, func(row audit.Row) bool { return row.EventID == target.EventID }) {
		t.Fatalf("waitForRows returned rows %s without the target node event %s", describeRows(rows), target.EventID)
	}
}

// coverageDeleteEvent returns a format 2 node.delete change event for a new
// project node as part of operation.
func coverageDeleteEvent(t *testing.T, operation audit.Event) audit.Event {
	t.Helper()
	target := audit.Target{
		Kind: audit.TargetKindNode, NodeType: "project", Reference: "", Name: "Coverage project", URI: "", RelationType: "",
		ID: uuid.Must(uuid.NewV7()), SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	change, err := audit.NewChange(uuid.Must(uuid.NewV7()), target, json.RawMessage(`{"name":"Coverage project"}`), nil)
	if err != nil {
		t.Fatalf("Creating the change for node %s failed: %v", target.ID, err)
	}
	event, err := audit.ChangeEvent(operation, change, clock.Now())
	if err != nil {
		t.Fatalf("Creating the change event for node %s failed: %v", target.ID, err)
	}
	return event
}
