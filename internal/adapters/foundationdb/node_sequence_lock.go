package foundationdb

import (
	"context"
	"fmt"
	"hash/maphash"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
)

// sequenceLockStripeCount is the fixed number of locks in one
// sequenceLockSet. Counters with keys that hash to the same stripe wait for
// each other, and the set uses the same memory for any number of counters.
const sequenceLockStripeCount = 256

// sequenceLockSet lets one process write each sequence counter in one
// transaction at a time, so concurrent writers in the process wait instead of
// conflicting in FoundationDB. Each stripe is a channel with room for one
// token. A writer puts a token in the channel before its transaction and
// removes the token after the transaction ends. Writers in other processes
// still conflict in FoundationDB and retry in the transaction retry loop.
type sequenceLockSet struct {
	seed    maphash.Seed
	stripes [sequenceLockStripeCount]chan struct{}
}

// newSequenceLockSet returns a set with every stripe free.
func newSequenceLockSet() *sequenceLockSet {
	var stripes [sequenceLockStripeCount]chan struct{}
	for i := range stripes {
		stripes[i] = make(chan struct{}, 1)
	}
	return &sequenceLockSet{seed: maphash.MakeSeed(), stripes: stripes}
}

// acquire waits until the stripe of the counter at key is free, takes it, and
// returns the function that frees it. When ctx ends first, acquire returns an
// error that wraps the context error and leaves the stripe unchanged.
func (set *sequenceLockSet) acquire(ctx context.Context, key fdb.Key) (func(), error) {
	stripe := set.stripes[maphash.Bytes(set.seed, key)%sequenceLockStripeCount]
	select {
	case stripe <- struct{}{}:
		return func() { <-stripe }, nil
	case <-ctx.Done():
		wrapped := fmt.Errorf("wait for the lock of sequence counter %q: %w", key, ctx.Err())
		slog.ErrorContext(ctx, "node.sequence_lock_failed", slog.String("err", wrapped.Error()))
		return nil, loggedSearchError{err: wrapped}
	}
}
