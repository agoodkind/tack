package integration_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// operationIDOf returns the operation ID that ctx identifies.
func operationIDOf(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	operation, err := audit.RequireMutationContext(ctx)
	if err != nil {
		t.Fatalf("read the audit operation: %v", err)
	}
	return operation.Context.OperationID
}

// labelEdge returns a labeled_with relationship from sourceID to labelID.
func labelEdge(env *integration.TestEnv, sourceID, labelID, actorID uuid.UUID) *node.Relationship {
	return &node.Relationship{
		OrgID: env.OrgID, SourceID: sourceID, RelationType: node.RelLabeledWith, TargetID: labelID, CreatedBy: actorID,
	}
}

// storedLabelEdge returns the JSON encoding of the stored labeled_with
// relationship from sourceID to labelID.
func storedLabelEdge(t *testing.T, env *integration.TestEnv, sourceID, labelID uuid.UUID) json.RawMessage {
	t.Helper()
	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, sourceID, node.RelLabeledWith)
	if err != nil {
		t.Fatalf("list labeled_with edges of %s: %v", sourceID, err)
	}
	for _, edge := range edges {
		if edge.TargetID != labelID {
			continue
		}
		encoded, err := json.Marshal(edge)
		if err != nil {
			t.Fatalf("encode the edge from %s to %s: %v", sourceID, labelID, err)
		}
		return encoded
	}
	t.Fatalf("node %s has no labeled_with edge to %s", sourceID, labelID)
	return nil
}

// storedNodeRecord returns the JSON encoding of the stored node record of
// nodeID.
func storedNodeRecord(t *testing.T, env *integration.TestEnv, nodeID uuid.UUID) json.RawMessage {
	t.Helper()
	current, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, nodeID)
	if err != nil || current == nil {
		t.Fatalf("read node %s: %+v, %v", nodeID, current, err)
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		t.Fatalf("encode node %s: %v", nodeID, err)
	}
	return encoded
}

// requireOneEvent returns the one valid event in events with verb and the
// target node ID or relationship source ID targetID.
func requireOneEvent(t *testing.T, events []audit.Event, verb audit.Verb, targetID uuid.UUID) audit.Event {
	t.Helper()
	matched := changeEventsFor(events, verb, targetID)
	if len(matched) != 1 {
		t.Fatalf("%s events for %s = %+v in %+v, want one", verb, targetID, matched, events)
	}
	if err := audit.ValidateEvent(matched[0]); err != nil {
		t.Fatalf("event %s is invalid: %v", matched[0].EventID, err)
	}
	return matched[0]
}

// requireRelationshipEvent requires event to target the relationship from
// sourceID to targetID with relationType.
func requireRelationshipEvent(t *testing.T, event audit.Event, sourceID uuid.UUID, relationType string, targetID uuid.UUID) {
	t.Helper()
	target := event.Context.Target
	if target == nil || target.Kind != audit.TargetKindRelationship || target.SourceID != sourceID ||
		target.RelationType != relationType || target.TargetID != targetID {
		t.Fatalf("event %s target = %+v, want %s %s %s", event.EventID, target, sourceID, relationType, targetID)
	}
}

// requireJSONEqual requires got and want to decode to the same JSON value.
func requireJSONEqual(t *testing.T, label string, got, want json.RawMessage) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("%s: decode %s: %v", label, got, err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("%s: decode %s: %v", label, want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("%s = %s, want %s", label, got, want)
	}
}
