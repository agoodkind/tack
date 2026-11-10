package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

var _ searchdomain.AccessVersionSessions = (*SearchAccessRolloutStore)(nil)

// HasActiveAccessVersion reports whether authorityID has a session presence
// entry under version in the current absolute-deadline minute or a later
// minute.
//
// Completion or closing removes the entry. The check reads a key range with
// a limit of one key and excludes earlier deadline minutes. It can still
// count a session after its absolute deadline within the current minute.
//
//   - A session that opens after an earlier version check can still block
//     BeginRetire, which reads presence again in its transaction.
func (s *SearchAccessRolloutStore) HasActiveAccessVersion(
	ctx context.Context,
	authorityID uuid.UUID,
	version string,
) (active bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rollout.version_active")(&err)
	now := s.work.clock.Now()
	err = transactSearch(
		ctx,
		s.db,
		func(tr fdb.Transaction) error {
			var readErr error
			active, readErr = sessionVersionPresent(ctx, tr, now, authorityID, version)

			return readErr
		},
	)
	if err != nil {
		return false, searchStorageError(
			ctx,
			"search.rollout.sessions_failed",
			"read sessions of access version "+version,
			uuid.Nil,
			err,
		)
	}

	return active, nil
}

// clearSessionVersion removes the version presence entry of record. A
// completed or closing session does not read another OpenSearch batch.
func clearSessionVersion(tr fdb.Transaction, record searchSessionRecord) {
	tr.Clear(
		fdb.Key(
			searchSessionVersionKey(
				record.AuthorityID,
				record.AccessVersion,
				record.AbsoluteDeadline,
				record.ID,
			),
		),
	)
}
