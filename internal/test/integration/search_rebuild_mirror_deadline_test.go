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

// TestSearchRebuildMirrorMissSurvivesExpiredSlice calls startMirrorRebuild and stores a node with
// several pages. The proxy delays the replacement index write of page 0.
// The test runs one worker slice with a context that expires after mirrorSliceDeadline.
// The serving index accepts page 0 before the deadline.
// The expired context ends the delayed mirror write. CompletePage fails with the expired context.
// The node content and the page 0 document do not change.
// The test calls requireSwitchAfterCopies and requireReplacementPages.
// The test requires a second replacement index write of the page 0 document at or after the
// abandonment time plus mirrorWriteHorizon minus one second of clock skew.
// A worker that calls RecordMirrorMiss with the expired context does not store the pending write
// record. The worker's next live claim writes the page 0 document to the replacement index before
// that time.
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
