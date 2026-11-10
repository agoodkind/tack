package foundationdb

import (
	"context"
	"encoding/base64"
	"log/slog"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

func verificationCursor(orgID uuid.UUID, documents []searchdomain.RolloutDocument) string {
	last := documents[len(documents)-1]
	key := searchIssuedKey(
		orgID,
		last.NodeID,
		last.Document.Revision,
		last.Document.Ordinal,
		last.Document.Projection,
	)

	return base64.RawURLEncoding.EncodeToString(key)
}

func logAffectedVerification(
	ctx context.Context,
	orgID uuid.UUID,
	generation int64,
	progress affectedProgress,
) {
	if !progress.ran {
		return
	}

	telemetry.L(ctx).InfoContext(
		ctx,
		"search.rollout.affected_progress",
		slog.String("authority_id", orgID.String()),
		slog.Int64("generation", generation),
		slog.Int("remaining_nodes", progress.remaining),
		slog.Bool("remaining_at_least", progress.capped),
	)
}
