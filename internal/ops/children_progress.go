package ops

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
)

func logDefaultChildCounts(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	typeKey string,
	parentCount int,
	createdCount int,
	skippedCount int,
) {
	env.Log.DebugContext(
		ctx,
		"backfill.type",
		slog.String("org_id", orgID.String()),
		slog.String("type", typeKey),
		slog.Int("parents", parentCount),
		slog.Int("children_created", createdCount),
		slog.Int("children_skipped_existing", skippedCount),
	)
}
