package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"goodkind.io/tack/internal/telemetry"
)

// deleteResumeInterval is the time between two checks for subtree delete
// jobs that lost their runner.
const deleteResumeInterval = time.Minute

// deleteStaleAfter is the age of the last step after which a subtree delete
// job counts as without a runner. One step is one FoundationDB transaction,
// and the deployed transaction timeout is far shorter.
const deleteStaleAfter = time.Minute

// deleteResumer is the loop that finishes subtree delete jobs after a crash.
type deleteResumer struct {
	cancel  context.CancelFunc
	loop    sync.WaitGroup
	started bool
}

// StartDeleteResumer starts the loop that finishes subtree delete jobs
// without a step in the last deleteStaleAfter. The loop stops when ctx ends
// or CloseContext runs.
func (g *Graph) StartDeleteResumer(ctx context.Context) {
	if g.deletes.started {
		return
	}
	g.deletes.started = true
	loopContext, cancel := context.WithCancel(ctx)
	g.deletes.cancel = cancel
	g.deletes.loop.Go(func() {
		defer recoverSearchWorker(loopContext, "node.subtree_delete.loop_panicked")
		ticker := time.NewTicker(deleteResumeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-loopContext.Done():
				return
			case <-ticker.C:
				g.resumeDeletes(loopContext)
			}
		}
	})
	telemetry.L(ctx).InfoContext(ctx, "node.subtree_delete.resumer_started")
}

// resumeDeletes runs one resume pass and recovers a panic in that pass. The
// service logs each failure at Error.
func (g *Graph) resumeDeletes(ctx context.Context) {
	defer func() {
		if recovered := recover(); recovered != nil {
			telemetry.L(ctx).ErrorContext(ctx, "node.subtree_delete.resume_panicked", slog.String("err", fmt.Sprint(recovered)))
		}
	}()
	_, _ = g.nodes.ResumeSubtreeDeletes(ctx, deleteStaleAfter)
}

// stopDeleteResumer stops the resume loop and waits for it.
func (g *Graph) stopDeleteResumer() {
	if g.deletes.cancel != nil {
		g.deletes.cancel()
	}
	if g.deletes.started {
		g.deletes.loop.Wait()
	}
}
