package integration_test

import (
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// retryCreateCount is the number of issues that
// TestNodeIntegrityMutationRetryRecordsOneEvent creates under a project.
const retryCreateCount = 20

// retryRewritesPerCreate limits the test to 3 project rewrites per create.
const retryRewritesPerCreate = 3

// retryRewritePause is the 2 millisecond pause after each project rewrite.
const retryRewritePause = 2 * time.Millisecond

// TestNodeIntegrityMutationRetryRecordsOneEvent creates 20 issues under a
// project. For each issue, the test starts NodeService.Create under an audit
// slot in a separate goroutine. The test commits 3 rewrites of the unchanged
// project record through NodeStore.Set and pauses for retryRewritePause
// after each rewrite. It then waits for the create.
//
// A rewrite that commits during a create attempt conflicts with that
// attempt's read of the project record. FoundationDB retries that attempt.
//
// The test limits project rewrites to 60 in total. It requires every create
// to succeed and exactly one node.create outbox event for each issue.
func TestNodeIntegrityMutationRetryRecordsOneEvent(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	project, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, projectID)
	if err != nil || project == nil {
		t.Fatalf("get project %s: %v, %v", projectID, project, err)
	}
	projectView := storedStoreView(t, env.Stores, projectID)
	rewrites := 0
	createdIDs := make([]uuid.UUID, 0, retryCreateCount)
	for position := range retryCreateCount {
		input := schemaCreateInput(projectID, projectID, "issue", nil)
		input.Name = "Retried issue " + strconv.Itoa(position)
		var created *service.CreateResult
		var createErr error
		var creator sync.WaitGroup
		creator.Go(func() {
			created, createErr = env.NodeSvc.Create(auditSlot(env, input.ActorID), input)
		})
		for range retryRewritesPerCreate {
			if err := env.Stores.Nodes.Set(env.Ctx, project, projectView); err != nil {
				creator.Wait()
				t.Fatalf("rewrite project %s: %v", projectID, err)
			}
			rewrites++
			time.Sleep(retryRewritePause)
		}
		creator.Wait()
		if createErr != nil {
			t.Fatalf("create issue %d during project rewrites: %v", position, createErr)
		}
		createdIDs = append(createdIDs, created.View.ID)
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
	t.Logf("the test committed %d project rewrites during %d creates", rewrites, retryCreateCount)
}
