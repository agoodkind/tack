package datagen

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

const (
	// searchMixedRetryLimit bounds the retries of one phase read.
	searchMixedRetryLimit = time.Minute
	// foundationDBTimedOut is the FoundationDB error code of a read that outlasts
	// its transaction timeout.
	foundationDBTimedOut = 1031
)

// retryTimedOutRead retries read after a FoundationDB timeout until
// searchMixedRetryLimit expires or ctx ends. It returns other errors without
// retrying and uses nodeID in retry logs.
func retryTimedOutRead(ctx context.Context, nodeID uuid.UUID, read func() error) error {
	deadline := clock.Now().Add(searchMixedRetryLimit)
	for {
		err := read()
		var databaseError fdb.Error
		timedOut := errors.As(err, &databaseError) && databaseError.Code == foundationDBTimedOut
		if !timedOut || clock.Now().After(deadline) || !waitForSearchMixedPoll(ctx) {
			return err
		}
		slog.DebugContext(ctx, "qa.datagen.search_mixed_read_retried", slog.String("node_id", nodeID.String()), slog.String("err", err.Error()))
	}
}

// resolveResult reads the resolve record of nodeID with retryTimedOutRead.
func (m *searchMixed) resolveResult(ctx context.Context, nodeID uuid.UUID) (*node.NodeResolve, error) {
	var resolved *node.NodeResolve
	err := retryTimedOutRead(ctx, nodeID, func() error {
		var readErr error
		resolved, readErr = m.stores.Views.Resolve(ctx, nodeID)
		return readErr
	})
	return resolved, err
}
