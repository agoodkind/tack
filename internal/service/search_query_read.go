package service

import (
	"context"
	"encoding/json"
	"errors"

	searchdomain "goodkind.io/tack/internal/domain/search"
)

// readBatch reads one raw batch at position. If the snapshot is lost, it
// closes the session and joins any closeSession error with the read failure.
func (s *SearchQueryService) readBatch(
	ctx context.Context,
	session searchdomain.Session,
	snapshot searchdomain.Snapshot,
	position json.RawMessage,
) (searchdomain.RankBatch, error) {
	raw, err := s.ports.Ranker.Read(ctx, session.Query, snapshot, position)
	if err != nil {
		var closeErr error
		if errors.Is(err, searchdomain.ErrSnapshotLost) {
			closeErr = s.closeSession(ctx, session)
		}

		failure := errors.Join(queryFailure(ctx, "read ranked batch", session.ID, err), closeErr)

		return searchdomain.RankBatch{}, failure
	}

	return raw, nil
}
