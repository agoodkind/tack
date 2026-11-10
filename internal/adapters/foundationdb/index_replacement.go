package foundationdb

import (
	"context"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

func newRebuildState(
	identifier uuid.UUID,
	request searchdomain.BeginRebuild,
	serving []byte,
	generation int64,
	now time.Time,
) searchdomain.Rebuild {
	return searchdomain.Rebuild{
		ID:            identifier,
		Mode:          request.Mode,
		State:         searchdomain.RebuildCreating,
		SourceIndex:   string(serving),
		TargetIndex:   replacementIndexPrefix + identifier.String(),
		ScanCursor:    "",
		VerifyCursor:  "",
		Failure:       "",
		PrimaryShards: request.PrimaryShards,
		RoutingShards: request.RoutingShards,
		Replicas:      request.Replicas,
		ScanComplete:  false,
		Paused:        false,
		Restored:      request.Restored,
		PausedAt:      now,
		Generation:    generation,
		Initiation:    request.Initiation,
	}
}

func beginRebuildGeneration(
	ctx context.Context,
	transaction fdb.Transaction,
	restored bool,
) (int64, error) {
	generation, counterErr := incrementSearchCounter(ctx, transaction, searchRebuildGenerationKey())
	if counterErr != nil {
		return 0, counterErr
	}

	if restored {
		if _, epochErr := incrementSearchCounter(
			ctx,
			transaction,
			searchRestoreEpochKey(),
		); epochErr != nil {
			return 0, epochErr
		}
	}

	return generation, nil
}
