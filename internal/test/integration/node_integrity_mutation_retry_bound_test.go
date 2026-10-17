package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// retryBoundLimit is the maximum duration of the refused subtree delete step
	// in TestNodeIntegrityMutationRetryBoundRefusesStep. It exceeds the test
	// stores' 5 second transaction timeout by 2 seconds.
	retryBoundLimit = 7 * time.Second
	// retryBoundDeadline is the context deadline for the steps in
	// TestNodeIntegrityMutationRetryBoundRefusesStep.
	retryBoundDeadline = 30 * time.Second
	// retryBoundStepLimit limits the number of subtree delete steps that the test
	// runs before the refused step.
	retryBoundStepLimit = 10
)

// TestNodeIntegrityMutationRetryBoundRefusesStep starts a subtree delete of an
// issue with one comment and runs NodeDeleteStore.DeleteSubtreeStep with a
// DeletionEventBuilder. Each transaction attempt reads the comment, then calls
// the builder. The builder commits NodeStore.Set of the comment in a separate
// transaction and makes every attempt conflict. The test covers the retry
// bound of runNodeMutation that every node mutation shares. The test requires
// the step to return an error that wraps domain.ErrConflict within
// retryBoundLimit after more than one attempt. It also requires the comment
// to still exist.
func TestNodeIntegrityMutationRetryBoundRefusesStep(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	issue := createSchemaNode(t, env, projectID, projectID, "issue", nil)
	createSchemaNode(t, env, issue.ID, projectID, "comment", schemaProps("description", quoted("A comment")))
	job := &node.SubtreeDeleteJob{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, RootID: issue.ID, ActorID: uuid.New(),
		AuditTemplate: json.RawMessage(`{}`), Stack: nil, Deleted: 0, UpdatedAt: time.Time{},
	}
	ctx, cancel := context.WithTimeout(env.Ctx, retryBoundDeadline)
	defer cancel()
	if err := env.Stores.NodeDeleter.StartSubtreeDelete(ctx, job); err != nil {
		t.Fatalf("start the subtree delete of issue %s: %v", issue.ID, err)
	}

	rewritten := []uuid.UUID{}
	rewrite := func(template json.RawMessage, change node.SubtreeChange) (json.RawMessage, error) {
		rewritten = append(rewritten, change.ID)
		current, err := env.Stores.Nodes.Get(ctx, env.OrgID, change.ID)
		if err != nil || current == nil {
			return nil, errors.Join(errors.New("read node "+change.ID.String()), err)
		}
		if err := env.Stores.Nodes.Set(ctx, current, storedStoreView(t, env.Stores, change.ID)); err != nil {
			return nil, err
		}
		return template, nil
	}
	var stepErr error
	var elapsed time.Duration
	for range retryBoundStepLimit {
		started := time.Now()
		progress, err := env.Stores.NodeDeleter.DeleteSubtreeStep(ctx, job.ID, rewrite)
		elapsed = time.Since(started)
		if err != nil || progress.Done {
			stepErr = err
			break
		}
	}

	if !errors.Is(stepErr, domain.ErrConflict) || elapsed > retryBoundLimit {
		t.Fatalf("subtree delete step after %s and %d builder calls returned %v, want an error that wraps domain.ErrConflict within %s",
			elapsed, len(rewritten), stepErr, retryBoundLimit)
	}
	if len(rewritten) < 2 {
		t.Fatalf("the builder ran %d times, want at least 2 attempts", len(rewritten))
	}
	if current, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, rewritten[0]); err != nil || current == nil {
		t.Fatalf("node %s after the refused step = %v, %v, want the stored node", rewritten[0], current, err)
	}
	t.Logf("the step returned after %s and %d builder calls", elapsed, len(rewritten))
}
