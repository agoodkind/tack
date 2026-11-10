package foundationdb

import (
	"context"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/telemetry"
)

// SearchSessions constructs the query session store on the shared
// connection.
//
// The store uses source to check session deadlines and idleTimeout when
// committing pages.
//
//   - Page commits cap the renewed idle deadline at the session's absolute
//     deadline.
func (s *Stores) SearchSessions(source clock.Clock, idleTimeout time.Duration) *SearchSessionStore {
	return NewSearchSessionStore(s.db, source, idleTimeout)
}

// SearchRollouts constructs the access policy rollout store on the shared
// connection with the injected clock.
//
// The store uses policies for access rollout validation and creates its work
// store with source.
//
//   - Rollout steps compare the claimed generation with stored work before
//     writing.
func (s *Stores) SearchRollouts(
	source clock.Clock,
	policies *searchaccess.PolicySet,
) *SearchAccessRolloutStore {
	return NewSearchAccessRolloutStore(s.db, NewSearchWorkStore(s.db, source), policies)
}

// SearchRebuilds constructs the index replacement store on the shared
// connection with the injected clock.
//
// Its session store deletes expired sessions and does not renew idle
// deadlines.
//
//   - The replacement store and its work and session stores use source.
func (s *Stores) SearchRebuilds(source clock.Clock) *SearchRebuildStore {
	return NewSearchRebuildStore(
		s.db,
		NewSearchWorkStore(s.db, source),
		NewSearchSessionStore(s.db, source, 0),
	)
}

// SearchRestoreEpoch reads the restore epoch that a restored index
// replacement increments.
//
// Every session records this epoch to check whether continuation remains
// valid after a restore.
//
//   - An absent counter returns zero.
//   - A stored counter uses the shared counter decoder.
func (s *Stores) SearchRestoreEpoch(ctx context.Context) (epoch int64, err error) {
	defer telemetry.FDBOp(ctx, "store.search_restore_epoch.read")(&err)
	var encoded []byte
	err = transactSession(
		ctx,
		s.db,
		s.Nodes.clock,
		"read search restore epoch",
		uuid.Nil,
		func(tr fdb.Transaction) error {
			value, readErr := tr.Get(fdb.Key(searchRestoreEpochKey())).Get()
			if readErr != nil {
				return sessionStepError{operation: "read search restore epoch", err: readErr}
			}

			encoded = value

			return nil
		},
	)
	if err != nil || len(encoded) == 0 {
		return 0, err
	}

	return unpackCounter(ctx, encoded)
}

// NodeSummaries constructs the bounded summary reader on the shared
// connection.
//
// The reader uses policies to compile current opaque access keys for each
// requested node.
//
//   - A summary request rejects more than maxSummaryBatch node IDs.
func (s *Stores) NodeSummaries(policies *searchaccess.PolicySet) *NodeSummaryStore {
	return NewNodeSummaryStore(s.db, policies)
}

// ServingSearchIndex reads the current serving physical index.
//
// The ops search provision command initializes the record. The function
// returns [searchdomain.ErrNoServingIndex] when the key does not record an
// index.
//
//   - Copying and verifying replacements continue to serve SourceIndex.
//   - A completed switch makes TargetIndex the result while the old index
//     retires.
//   - A failed database read returns an empty string with its storage error.
func (s *Stores) ServingSearchIndex(ctx context.Context) (index string, err error) {
	defer telemetry.FDBOp(ctx, "store.search_index.read")(&err)
	err = transactSession(
		ctx,
		s.db,
		s.Nodes.clock,
		"read serving search index",
		uuid.Nil,
		func(tr fdb.Transaction) error {
			value, readErr := tr.Get(fdb.Key(searchIndexKey())).Get()
			if readErr != nil {
				return sessionStepError{operation: "read serving search index", err: readErr}
			}

			index = string(value)

			return nil
		},
	)
	if err != nil {
		return "", err
	}

	if index == "" {
		return "", searchdomain.ErrNoServingIndex
	}

	return index, nil
}
