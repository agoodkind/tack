package integration_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

// TestSearchRebuildRefusesMissingOriginalOperation verifies that an earlier
// in-flight record cannot lose its actor or claim completion without evidence.
func TestSearchRebuildRefusesMissingOriginalOperation(t *testing.T) {
	env := integration.SetupTestEnv(t)
	if err := env.Stores.InitializeSearchIndex(env.Ctx, reindexServingIndex); err != nil {
		t.Fatalf("initialize serving index: %v", err)
	}

	store := env.Stores.SearchRebuilds(clock.Wall{})
	creating, err := store.BeginRebuild(
		env.Ctx,
		searchdomain.BeginRebuild{
			Mode:          searchdomain.ReplacementFull,
			PrimaryShards: 1,
			RoutingShards: 24,
			Replicas:      0,
			Restored:      false,
			Reason:        "missing original operation",
			Initiation: searchdomain.Initiation{
				OperationID: uuid.Must(uuid.NewV7()),
				ActorID:     integration.TestActorID,
				Operation:   nil,
			},
		},
	)
	if err != nil {
		t.Fatalf("start earlier in-flight rebuild: %v", err)
	}

	work, claimed, err := env.Stores.SearchWork(clock.Wall{}).Claim(
		env.Ctx,
		searchdomain.WorkClassRebuild,
		"original-operation-proof",
		time.Minute,
	)
	if err != nil || !claimed {
		t.Fatalf("claim earlier rebuild: claimed %t, error %v", claimed, err)
	}

	retiring := creating
	retiring.State = searchdomain.RebuildRetiring
	if err := store.AdvanceRebuild(env.Ctx, work, creating, retiring); err != nil {
		t.Fatalf("record earlier retiring rebuild: %v", err)
	}

	mark := outboxTail(t, env)
	err = store.FinishRebuild(env.Ctx, work, retiring)
	if !errors.Is(err, audit.ErrMissingMutationContext) ||
		!strings.Contains(
			err.Error(),
			"finish pending search work on the previous release before upgrading",
		) {
		t.Fatalf("finish lacks the original-operation refusal and recovery action: %v", err)
	}

	current, found, err := store.CurrentRebuild(env.Ctx)
	if err != nil || !found || current.ID != retiring.ID || current.State != retiring.State {
		t.Fatalf(
			"refused finish did not retain the rebuild: found %t, state %+v, error %v",
			found,
			current,
			err,
		)
	}

	if events := outboxEventsAfter(t, env, mark); len(events) != 0 {
		t.Fatalf("refused finish invented %d audit events", len(events))
	}
}
