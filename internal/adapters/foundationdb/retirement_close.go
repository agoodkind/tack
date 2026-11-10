package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

func (s *SearchRebuildStore) beginRetireCleanup(
	ctx context.Context,
	index string,
	sessionID uuid.UUID,
	record searchSessionRecord,
) error {
	if record.Closing {
		return nil
	}

	return s.retireStep(
		ctx,
		index,
		sessionID,
		record,
		searchdomain.CleanupClose,
		func(stepContext context.Context, fence fdb.Key) error {
			return s.sessions.beginCleanup(stepContext, sessionID, fence)
		},
	)
}
