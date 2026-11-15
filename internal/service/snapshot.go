package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

func (s *SearchQueryService) closeSnapshot(
	ctx context.Context,
	snapshot searchdomain.Snapshot,
) error {
	if snapshot.PITID == "" {
		return nil
	}

	err := s.ports.Ranker.Close(ctx, snapshot)
	if errors.Is(err, searchdomain.ErrSnapshotLost) {
		return nil
	}

	if err != nil {
		wrapped := fmt.Errorf("close search snapshot %s: %w", snapshot.PITID, err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.snapshot.close_failed",
			slog.String("err", wrapped.Error()),
		)

		return wrapped
	}

	return nil
}
