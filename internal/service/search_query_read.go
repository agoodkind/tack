package service

import (
	"context"
	"encoding/json"
	"errors"

	searchdomain "goodkind.io/tack/internal/domain/search"
)

// readBatch reads one ranked batch and closes a session with a lost snapshot.
func (s *SearchQueryService) readBatch(
	ctx context.Context,
	session searchdomain.Session,
	snapshot searchdomain.Snapshot,
	position json.RawMessage,
) (searchdomain.RankBatch, error) {
	raw, err := s.ports.Ranker.Read(ctx, session.Query, snapshot, position)
	if err != nil {
		if errors.Is(err, searchdomain.ErrSnapshotLost) {
			s.closeSession(ctx, session)
		}

		return searchdomain.RankBatch{}, queryFailure(ctx, "read ranked batch", session.ID, err)
	}

	return raw, nil
}
