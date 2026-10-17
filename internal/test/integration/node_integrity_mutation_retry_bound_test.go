package integration_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/test/integration"
)

// boundCreateDeadline limits the wait for NodeService.Create to return in
// TestNodeIntegrityMutationRetryBoundRefusesCreate.
const boundCreateDeadline = 30 * time.Second

// boundCreateOperation specifies the operation text that
// TestNodeIntegrityMutationRetryBoundRefusesCreate requires in the create
// error.
const boundCreateOperation = "create node: apply mutation to node "

// TestNodeIntegrityMutationRetryBoundRefusesCreate calls NodeService.Create
// for an issue under a project while another goroutine repeatedly rewrites
// the unchanged project record through NodeStore.Set without a pause.
//
// The test requires the create to return within boundCreateDeadline with an
// error that wraps domain.ErrConflict and contains boundCreateOperation.
// It also requires that the project does not contain an issue after the
// create returns.
func TestNodeIntegrityMutationRetryBoundRefusesCreate(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	project, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, projectID)
	if err != nil || project == nil {
		t.Fatalf("get project %s: %v, %v", projectID, project, err)
	}
	projectView := storedStoreView(t, env.Stores, projectID)
	stop := make(chan struct{})
	rewrites := 0
	var rewriteErr error
	var rewriter sync.WaitGroup
	rewriter.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := env.Stores.Nodes.Set(env.Ctx, project, projectView); err != nil {
				rewriteErr = err
				return
			}
			rewrites++
		}
	})
	input := schemaCreateInput(projectID, projectID, "issue", nil)
	result := make(chan error, 1)
	go func() {
		_, createErr := env.NodeSvc.Create(auditSlot(env, input.ActorID), input)
		result <- createErr
	}()
	started := time.Now()
	var createErr error
	returned := true
	select {
	case createErr = <-result:
	case <-time.After(boundCreateDeadline):
		returned = false
	}
	elapsed := time.Since(started)
	close(stop)
	rewriter.Wait()
	if !returned {
		createErr = <-result
		t.Fatalf("create did not return during %s of project rewrites, and after the rewrites stopped it returned %v", boundCreateDeadline, createErr)
	}
	if rewriteErr != nil {
		t.Fatalf("rewrite project %s after %d rewrites: %v", projectID, rewrites, rewriteErr)
	}
	if !errors.Is(createErr, domain.ErrConflict) || !strings.Contains(createErr.Error(), boundCreateOperation) {
		t.Fatalf("create error = %v, want a conflict that contains %q", createErr, boundCreateOperation)
	}
	if issues := listByScope(t, env, "issue", projectID); len(issues) != 0 {
		t.Fatalf("project %s has %d issues after the refused create, want 0", projectID, len(issues))
	}
	t.Logf("the create returned after %s and %d project rewrites", elapsed, rewrites)
}
