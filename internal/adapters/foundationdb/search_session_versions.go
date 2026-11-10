package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

var _ searchdomain.AccessVersionSessions = (*SearchAccessRolloutStore)(nil)

// HasActiveAccessVersion reports whether any session of authorityID can
// still read OpenSearch under version. A session counts until it completes,
// closes, or passes its absolute deadline. The check reads a key range with a
// limit of one key.
func (s *SearchAccessRolloutStore) HasActiveAccessVersion(ctx context.Context, authorityID uuid.UUID, version string) (active bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rollout.version_active")(&err)
	now := s.work.clock.Now()
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		var readErr error
		active, readErr = sessionVersionPresent(ctx, tr, now, authorityID, version)
		return readErr
	})
	if err != nil {
		return false, searchStorageError(ctx, "search.rollout.sessions_failed", "read sessions of access version "+version, uuid.Nil, err)
	}
	return active, nil
}

// clearSessionVersion removes the version presence entry of record. A
// completed or closing session reads no further OpenSearch batch.
func clearSessionVersion(tr fdb.Transaction, record searchSessionRecord) {
	tr.Clear(fdb.Key(searchSessionVersionKey(record.AuthorityID, record.AccessVersion, record.AbsoluteDeadline, record.ID)))
}
