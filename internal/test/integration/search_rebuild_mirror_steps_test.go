package integration_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// duringFirstHold runs on the test goroutine before runTwoMirrorMisses stores the node.
// duringFirstHold returns an action that runs in another goroutine while the proxy delays
// the replacement index write of page 1. The action returns an error.
// afterFirstMiss runs after the worker abandons that write.
type mirrorSteps struct {
	duringFirstHold func(*testing.T, *migrationEnv, uuid.UUID) func(context.Context) error
	afterFirstMiss  func(*testing.T, *migrationEnv, uuid.UUID)
}

// A relationship change raises the node generation during an abandoned mirror
// write without changing the revision or document identity.
// The stale claim cannot checkpoint. Its replacement must preserve the original
// deadline before resending that document.
func TestSearchRebuildMirrorMissSurvivesFailedCheckpoint(t *testing.T) {
	run := runTwoMirrorMisses(t, mirrorSteps{duringFirstHold: relateNode, afterFirstMiss: nil})
	requireSwitchAfterCopies(t, run.env, run.target)
	requireReplacementPages(t, run)
	requireResendAfterHorizon(t, run.proxy, run.nodeID, mirrorFirstOrdinal, run.target, run.firstAbandoned)
}

// Create the action on the test goroutine before running it during the held request.
// Return its error or recovered panic to the waiting test goroutine.
func (steps mirrorSteps) startDuringHold(t *testing.T, env *migrationEnv, proxy *pageDelayProxy, nodeID uuid.UUID) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	if steps.duringFirstHold == nil {
		done <- nil
		return done
	}
	action := steps.duringFirstHold(t, env, nodeID)
	proxy.mutex.Lock()
	started := proxy.started
	proxy.mutex.Unlock()
	ctx := integration.WithTestOperation(t, t.Context())
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- fmt.Errorf("change during the delayed write panicked: %v", recovered)
			}
		}()
		<-started
		done <- action(ctx)
	}()
	return done
}

// Create the related node on the test goroutine.
// The returned relationship change raises search generation without changing revision.
// Return write errors to the waiting test goroutine.
func relateNode(t *testing.T, env *migrationEnv, nodeID uuid.UUID) func(context.Context) error {
	t.Helper()
	otherID := uuid.Must(uuid.NewV7())
	env.putNode(t, otherID, shortTimingText)
	relationship := &node.Relationship{
		OrgID: env.OrgID, SourceID: nodeID, RelationType: uniqueKey("r"), TargetID: otherID,
		CreatedBy: uuid.Nil, CreatedAt: clock.Now().UTC(), Props: nil,
	}
	return func(ctx context.Context) error {
		if err := env.Stores.Relationships.Add(ctx, relationship); err != nil {
			return fmt.Errorf("add a relationship of node %s: %w", nodeID, err)
		}
		return nil
	}
}

func raiseNodeGeneration(t *testing.T, env *migrationEnv, nodeID uuid.UUID) {
	t.Helper()
	if err := relateNode(t, env, nodeID)(integration.WithTestOperation(t, t.Context())); err != nil {
		t.Fatal(err)
	}
}

// targetIn selects a page write by nodeID, ordinal, and physical index
// and clears the previous delay state. pageDelayProxy delays the next matching write.
func (proxy *pageDelayProxy) targetIn(nodeID uuid.UUID, ordinal uint64, index string) {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	proxy.nodeID, proxy.ordinal, proxy.index = nodeID.String(), ordinal, index
	proxy.delayed, proxy.started = false, make(chan struct{})
	proxy.heldFrom, proxy.heldUntil, proxy.abandoned = time.Time{}, time.Time{}, false
}
