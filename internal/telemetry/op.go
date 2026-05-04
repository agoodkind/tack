package telemetry

import (
	"context"

	"goodkind.io/gklog/trace"
)

// Op is the defer-friendly storage/resolver timer. Defers to trace.Op so
// every consumer of gklog gets the same op semantics.
//
//	func (s *NodeStore) Get(ctx context.Context, ...) (n *node.Node, err error) {
//	    defer telemetry.Op(ctx, "store.node.get")(&err)
//	    // ...
//	}
func Op(ctx context.Context, name string) func(err *error) {
	return trace.Op(ctx, name)
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
	end := trace.Op(ctx, name)
	return func(errp *error) {
		if errp != nil && *errp != nil {
			IncFDBTxErr()
		} else {
			IncFDBTx()
		}
		end(errp)
	}
}
