package integration_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
)

// sequenceRefusalKey is the counter key that the refusal case tries to change.
const sequenceRefusalKey = "AUDIT-REFUSED-"

// TestRuntimeAuditSequenceCounterChange creates two issues in one project
// through the node service. Each create must write one sequence counter
// change event with the operation ID and actor of the issue's node.create
// event. The first event must record the counter's creation with the value
// 1. The second must record the change from 1 to 2.
func TestRuntimeAuditSequenceCounterChange(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	for position, want := range []struct {
		verb          audit.Verb
		before, after string
	}{
		{verb: audit.VerbSequenceCreate, before: "", after: `{"value":1}`},
		{verb: audit.VerbSequenceUpdate, before: `{"value":1}`, after: `{"value":2}`},
	} {
		mark := outboxTail(t, env)
		issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
		events := outboxEventsAfter(t, env, mark)
		created := requireOneEvent(t, events, audit.VerbNodeCreate, issue.ID)
		counter := requireOneCounterEvent(t, events)
		if counter.Verb != string(want.verb) || counter.Delta == nil ||
			string(counter.Delta.Before) != want.before || string(counter.Delta.After) != want.after {
			t.Fatalf("counter event of issue %d = %s %+v, want %s from %q to %q", position+1, counter.Verb, counter.Delta, want.verb, want.before, want.after)
		}
		requireChangeEvent(t, counter, created.Context.OperationID, created.Actor.ID)
	}
}

// TestRuntimeAuditSequenceRequiresMutationContext calls each sequence counter
// writer with a context that lacks a mutation operation. Each call must fail
// with audit.ErrMissingMutationContext, the counter must keep its value, and
// the outbox must not receive an event.
func TestRuntimeAuditSequenceRequiresMutationContext(t *testing.T) {
	env := integration.SetupTestEnv(t)
	if _, err := env.Stores.Nodes.AllocateSequenceByKey(env.Ctx, env.OrgID, sequenceRefusalKey); err != nil {
		t.Fatalf("allocate counter %s with a mutation operation: %v", sequenceRefusalKey, err)
	}
	mark := outboxTail(t, env)
	unaudited := t.Context()
	nodes := env.Stores.Nodes
	_, allocateErr := nodes.AllocateSequence(unaudited, env.OrgID, uuid.Must(uuid.NewV7()), "issue")
	_, allocateByKeyErr := nodes.AllocateSequenceByKey(unaudited, env.OrgID, sequenceRefusalKey)
	seedErr := nodes.SeedSequenceByKey(unaudited, env.OrgID, sequenceRefusalKey, 50)
	_, raiseErr := nodes.RaiseSequenceByKey(unaudited, env.OrgID, sequenceRefusalKey, 60)
	for name, err := range map[string]error{
		"AllocateSequence": allocateErr, "AllocateSequenceByKey": allocateByKeyErr,
		"SeedSequenceByKey": seedErr, "RaiseSequenceByKey": raiseErr,
	} {
		if !errors.Is(err, audit.ErrMissingMutationContext) {
			t.Fatalf("%s without a mutation operation = %v, want audit.ErrMissingMutationContext", name, err)
		}
	}
	value, err := nodes.PeekSequenceByKey(env.Ctx, env.OrgID, sequenceRefusalKey)
	if err != nil || value != 1 {
		t.Fatalf("counter %s = %d, %v after the refused writes, want 1", sequenceRefusalKey, value, err)
	}
	if events := outboxEventsAfter(t, env, mark); len(events) != 0 {
		t.Fatalf("the refused writes added outbox events %+v", events)
	}
}

// requireOneCounterEvent returns the one sequence counter change event in
// events.
func requireOneCounterEvent(t *testing.T, events []audit.Event) audit.Event {
	t.Helper()
	var counters []audit.Event
	for _, event := range events {
		target := event.Context.Target
		if target != nil && target.Kind == audit.TargetKindResource && target.NodeType == audit.TargetSequenceCounter {
			counters = append(counters, event)
		}
	}
	if len(counters) != 1 {
		t.Fatalf("sequence counter events = %+v, want one", counters)
	}
	return counters[0]
}
