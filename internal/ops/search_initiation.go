package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// commandInitiation returns the operation ID and actor ID of the operator
// command that runs with ctx. The search record that the command begins
// stores them, and the search worker records them in each later step.
func commandInitiation(ctx context.Context) (searchdomain.Initiation, error) {
	operation, err := audit.RequireMutationContext(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "search.initiation.missing", slog.String("err", err.Error()))
		return searchdomain.Initiation{}, fmt.Errorf("read the operator command operation: %w", err)
	}
	return searchdomain.Initiation{OperationID: operation.Context.OperationID, ActorID: operation.Actor.ID}, nil
}
