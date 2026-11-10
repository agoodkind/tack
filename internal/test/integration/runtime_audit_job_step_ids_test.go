package integration_test

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// TestRuntimeAuditJobStepEventIDs starts a subtree delete of an issue with a
// child issue and saves every key under the test prefix, including the job
// record. It runs one step, restores the saved keys while it keeps the
// outbox entries of that step, and runs the same step again. Both runs must
// write the child's move events with the same event IDs in the same order,
// because a step derives each event ID from the job's operation ID, the job
// ID, the change kind, and the record identity. The production relay and
// audit consumer then deliver both copies of each event, and the ledger must
// contain exactly one row for each event ID.
func TestRuntimeAuditJobStepEventIDs(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	root := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	child := createSchemaNode(t, env, root.ID, projectID, "issue", nil)
	job := &node.SubtreeDeleteJob{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, RootID: root.ID, ActorID: integration.TestActorID,
		Stack: nil, Deleted: 0, UpdatedAt: time.Time{},
	}
	if err := env.Stores.NodeDeleter.StartSubtreeDelete(env.Ctx, job); err != nil {
		t.Fatalf("start the delete of issue %s: %v", root.ID, err)
	}
	database, err := fdbadapter.Open(testenv.FoundationDB(t), prefixCopyTimeout)
	if err != nil {
		t.Fatalf("open FoundationDB: %v", err)
	}
	saved := readTestKeys(t, database, testPrefixKeys(t))

	first := stepEventIDs(t, env, job.ID, outboxTail(t, env), child.ID)
	firstOutbox := readTestKeys(t, database, testOutboxKeys(t))
	restoreTestPrefix(t, database, slices.Concat(saved, firstOutbox))
	second := stepEventIDs(t, env, job.ID, outboxTail(t, env), child.ID)
	if !slices.Equal(first, second) {
		t.Fatalf("event IDs of the repeated step = %v, want the first run's IDs %v", second, first)
	}

	ledger := startNodeAuditLedger(t)
	waitForEmptyOutbox(t, env)
	ledger.waitForNoLag(t)
	for _, eventID := range first {
		var rows int
		if err := ledger.admin.QueryRow(t.Context(), eventRowsQuery, eventID).Scan(&rows); err != nil || rows != 1 {
			t.Fatalf("ledger rows for event %s = %d, %v, want 1", eventID, rows, err)
		}
	}
}

// stepEventIDs runs one step of jobID and returns the IDs of the change events
// that the step wrote after mark. It requires the step to move childID.
func stepEventIDs(t *testing.T, env *integration.TestEnv, jobID uuid.UUID, mark []byte, childID uuid.UUID) []uuid.UUID {
	t.Helper()
	if _, err := env.Stores.NodeDeleter.DeleteSubtreeStep(env.Ctx, jobID); err != nil {
		t.Fatalf("run one step of job %s: %v", jobID, err)
	}
	events := outboxEventsAfter(t, env, mark)
	requireOneEvent(t, events, audit.VerbNodeUpdate, childID)
	eventIDs := make([]uuid.UUID, 0, len(events))
	for _, event := range events {
		eventIDs = append(eventIDs, event.EventID)
	}
	return eventIDs
}

// waitForEmptyOutbox waits until the relay has published and deleted every
// FoundationDB outbox entry under the test prefix.
func waitForEmptyOutbox(t *testing.T, env *integration.TestEnv) {
	t.Helper()
	deadline := clock.Now().Add(nodeAuditDeadline)
	for len(outboxEventsAfter(t, env, nil)) > 0 {
		if clock.Now().After(deadline) {
			t.Fatalf("the FoundationDB outbox still has entries after %s", nodeAuditDeadline)
		}
		time.Sleep(nodeAuditPollInterval)
	}
}
