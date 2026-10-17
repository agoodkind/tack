package integration_test

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/test/integration"
)

// retryCreateCount is the number of issues that
// TestNodeIntegrityMutationRetryRecordsOneEvent creates under a project.
const retryCreateCount = 20

// TestNodeIntegrityMutationRetryRecordsOneEvent creates 20 issues under a
// project through NodeService.Create under audit slots. Another goroutine
// repeatedly rewrites the unchanged project record through NodeStore.Set.
// A rewrite that commits during a create attempt conflicts with that
// attempt's read of the project record. FoundationDB then retries the attempt.
//
// The test requires every create to succeed and at least one rewrite to
// commit. It requires exactly one node.create outbox event for each issue.
func TestNodeIntegrityMutationRetryRecordsOneEvent(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	project, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, projectID)
	if err != nil || project == nil {
		t.Fatalf("get project %s: %v, %v", projectID, project, err)
	}
	projectView := storedStoreView(t, env.Stores, projectID)
	rewriteContext, stopRewrites := context.WithCancel(env.Ctx)
	var rewrites atomic.Int64
	var rewriter sync.WaitGroup
	rewriter.Go(func() {
		for rewriteContext.Err() == nil {
			if err := env.Stores.Nodes.Set(env.Ctx, project, projectView); err != nil {
				t.Errorf("rewrite project %s: %v", projectID, err)
				return
			}
			rewrites.Add(1)
		}
	})

	createdIDs := make([]uuid.UUID, 0, retryCreateCount)
	for position := range retryCreateCount {
		input := schemaCreateInput(projectID, projectID, "issue", nil)
		input.Name = "Retried issue " + strconv.Itoa(position)
		created, err := env.NodeSvc.Create(auditSlot(env, input.ActorID), input)
		if err != nil {
			stopRewrites()
			t.Fatalf("create issue %d during project rewrites: %v", position, err)
		}
		createdIDs = append(createdIDs, created.View.ID)
	}
	stopRewrites()
	rewriter.Wait()
	if rewrites.Load() == 0 {
		t.Fatalf("the project rewriter committed no rewrite during %d creates", retryCreateCount)
	}
	for _, createdID := range createdIDs {
		var event audit.Event
		if err := json.Unmarshal(outboxEvent(t, env.Stores, createdID, audit.VerbNodeCreate), &event); err != nil {
			t.Fatalf("decode the node.create event of %s: %v", createdID, err)
		}
		if event.Entity.ID != createdID || outboxEventCount(t, env, createdID) != 1 {
			t.Fatalf("outbox events of issue %s = %+v, want exactly one node.create event", createdID, event)
		}
	}
	t.Logf("the project rewriter committed %d rewrites during %d creates", rewrites.Load(), retryCreateCount)
}
