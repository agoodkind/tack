package telemetry

import (
	"context"
	"log/slog"
	"time"
)

// SlowOpThreshold is the latency above which Op emits a "slow" warning. Tune
// from real metric data once the server has been running with metrics for a
// while. Initial value is a guess.
var SlowOpThreshold = 50 * time.Millisecond

// Op starts a defer-friendly timer for one storage or resolver operation.
// Use:
//
//	func (s *NodeStore) Get(ctx context.Context, ...) (n *node.Node, err error) {
//	    defer telemetry.Op(ctx, "store.node.get")(&err)
//	    // ...
//	}
//
// On the deferred call, Op emits one of three structured events:
//   - debug: <name> with duration_ms when the op completed under the slow
//     threshold and err was nil
//   - warn: <name> with status="slow" + duration_ms when the op exceeded
//     SlowOpThreshold and err was nil
//   - warn: <name> with status="failed" + err on non-nil err
//
// The Op event name is the dot-separated identifier passed in. Callers
// should use stable names like "store.node.create_atomic" or
// "resolver.scope.lookup".
func Op(ctx context.Context, name string) func(err *error) {
	start := time.Now()
	return func(errp *error) {
		dur := time.Since(start)
		log := L(ctx).With(
			slog.String("op", name),
			slog.Int64("duration_ms", dur.Milliseconds()),
		)
		switch {
		case errp != nil && *errp != nil:
			log.WarnContext(ctx, name,
				slog.String("status", "failed"),
				slog.String("err", (*errp).Error()),
			)
		case dur > SlowOpThreshold:
			log.WarnContext(ctx, name, slog.String("status", "slow"))
		default:
			log.DebugContext(ctx, name, slog.String("status", "ok"))
		}
	}
}

// FDBOp wraps Op with FDB-specific metric counters. Use only from
// internal/adapters/foundationdb. Idiomatic call site:
//
//	func (s *NodeStore) Get(ctx context.Context, ...) (n *node.Node, err error) {
//	    defer telemetry.FDBOp(ctx, "store.node.get")(&err)
//	    // ...
//	}
//
// Bumps fdb_tx_total on success and fdb_tx_err_total on failure in addition
// to emitting the Op event.
func FDBOp(ctx context.Context, name string) func(err *error) {
	end := Op(ctx, name)
	return func(errp *error) {
		if errp != nil && *errp != nil {
			IncFDBTxErr()
		} else {
			IncFDBTx()
		}
		end(errp)
	}
}
