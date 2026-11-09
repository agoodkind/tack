package audit

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// ManifestPartDigest contains a part index and its SHA-256 digest.
type ManifestPartDigest struct {
	Index  uint32
	Digest string
}

// ManifestPartDigests reads part indexes and digests for one org and event in index order. It does not read payloads.
func (r *Reader) ManifestPartDigests(
	ctx context.Context,
	orgID uuid.UUID,
	eventID uuid.UUID,
) ([]ManifestPartDigest, error) {
	rows, err := r.pool.Rows(
		r.pool.unsanitized.Query(ctx, readStatement("manifest_part_digests"), orgID, eventID),
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.reader.manifest_digests_query_failed",
			slog.String("event_id", eventID.String()),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("read audit event %s manifest part digests: %w", eventID, err)
	}
	defer rows.Close()
	digests := make([]ManifestPartDigest, 0)

	for rows.Next() {
		digest := ManifestPartDigest{Index: 0, Digest: ""}
		if err := rows.Scan(&digest.Index, &digest.Digest); err != nil {
			slog.ErrorContext(
				ctx,
				"audit.reader.manifest_digests_scan_failed",
				slog.String("event_id", eventID.String()),
				slog.String("err", err.Error()),
			)
			return nil, fmt.Errorf("scan audit event %s manifest part digests: %w", eventID, err)
		}

		digests = append(digests, digest)
	}

	if err := rows.Err(); err != nil {
		slog.ErrorContext(
			ctx,
			"audit.reader.manifest_digests_scan_failed",
			slog.String("event_id", eventID.String()),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("read audit event %s manifest part digest rows: %w", eventID, err)
	}

	return digests, nil
}
