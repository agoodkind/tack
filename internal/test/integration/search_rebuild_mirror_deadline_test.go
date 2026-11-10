package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/test/integration"
)

const (
	mirrorExpiredOrdinal = 0
	// This slice deadline must expire before the worker's operation timeout.
	mirrorSliceDeadline = 6 * time.Second
)

// The slice expires after the serving write and before the mirror request ends.
// The expired checkpoint context must not prevent preservation of the original
// mirror deadline for the unchanged document.
func TestSearchRebuildMirrorMissSurvivesExpiredSlice(t *testing.T) {
	env, proxy, target := startMirrorRebuild(t)
	nodeID := uuid.Must(uuid.NewV7())
	proxy.targetIn(nodeID, mirrorExpiredOrdinal, target)
	env.putNode(t, nodeID, strings.Repeat(timingText, 40))
	ctx, cancel := context.WithTimeout(integration.WithTestOperation(t, t.Context()), mirrorSliceDeadline)
	claimed, err := env.Worker.RunSlice(ctx)
	cancel()
	_, abandonedAt, abandoned := proxy.held()
	if !claimed || !abandoned {
		t.Fatalf("the expired slice claimed %t and abandoned the write of page %d %t (error %v), want both",
			claimed, mirrorExpiredOrdinal, abandoned, err)
	}
	run := mirrorRun{env: env, proxy: proxy, target: target, nodeID: nodeID, firstAbandoned: abandonedAt, secondAbandoned: time.Time{}}
	requireSwitchAfterCopies(t, env, target)
	requireReplacementPages(t, run)
	requireResendAfterHorizon(t, proxy, nodeID, mirrorExpiredOrdinal, target, abandonedAt)
}
