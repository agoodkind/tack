package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

type referenceKeyReader interface {
	LookupReference(context.Context, uuid.UUID, string, string) (uuid.UUID, error)
}

// unheldReferenceKeys returns the keys whose node does not hold them yet, read
// before any write so the answer describes the state the write changes. The
// repair records one ledger event per key it claims, and a claim the node
// already held is not a change.
func unheldReferenceKeys(
	ctx context.Context,
	reader referenceKeyReader,
	orgID uuid.UUID,
	keys []repairedReferenceKey,
) ([]ReferenceKeyWrite, error) {
	unheld := make([]ReferenceKeyWrite, 0, len(keys))
	for _, key := range keys {
		holder, err := reader.LookupReference(ctx, orgID, key.Key.TemplateName, key.Key.Encoded)
		if err != nil {
			wrapped := fmt.Errorf("look up reference %q for node %s: %w", key.Key.Encoded, key.View.ID, err)
			telemetry.L(ctx).WarnContext(ctx, "repair.reference_uniqueness.key_lookup_failed",
				slog.String("node_id", key.View.ID.String()), slog.String("err", wrapped.Error()))
			return nil, wrapped
		}
		if holder == key.View.ID {
			continue
		}
		unheld = append(unheld, ReferenceKeyWrite{
			OrgID: orgID, NodeID: key.View.ID, NodeType: key.View.NodeType,
			TemplateName: key.Key.TemplateName, Encoded: key.Key.Encoded,
		})
	}
	return unheld, nil
}
