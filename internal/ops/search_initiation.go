package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// commandInitiation preserves the command event and its actor and operation IDs
// for the final result of the requested search work.
func commandInitiation(ctx context.Context) (searchdomain.Initiation, error) {
	operation, err := audit.RequireMutationContext(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "search.initiation.missing", slog.String("err", err.Error()))
		return searchdomain.Initiation{}, fmt.Errorf("read the operator command operation: %w", err)
	}

	encoded, err := audit.MarshalEvent(operation)
	if err != nil {
		slog.ErrorContext(ctx, "search.initiation.encode_failed", slog.String("err", err.Error()))
		return searchdomain.Initiation{}, fmt.Errorf("encode search command operation: %w", err)
	}

	return searchdomain.Initiation{
		OperationID: operation.Context.OperationID,
		ActorID:     operation.Actor.ID,
		Operation:   encoded,
	}, nil
}
