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
	// mirrorExpiredOrdinal equals the page ordinal of the replacement index write that the proxy delays
	// in TestSearchRebuildMirrorMissSurvivesExpiredSlice.
	mirrorExpiredOrdinal = 0
	// mirrorSliceDeadline limits the slice context to six seconds in
	// TestSearchRebuildMirrorMissSurvivesExpiredSlice.
	// The slice context expires after the serving index accepts page 0 and while the proxy delays the
	// replacement index write of page 0.
	// The six-second deadline is shorter than the ten-second OperationTimeout.
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
