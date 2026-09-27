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

// SearchSessions constructs the query session store on the shared connection.
func (s *Stores) SearchSessions(source clock.Clock, idleTimeout time.Duration) *SearchSessionStore {
	return NewSearchSessionStore(s.db, source, idleTimeout)
}

// SearchRollouts constructs the access policy rollout store on the shared
// connection with the injected clock.
func (s *Stores) SearchRollouts(source clock.Clock, policies *searchaccess.PolicySet) *SearchAccessRolloutStore {
	return NewSearchAccessRolloutStore(s.db, NewSearchWorkStore(s.db, source), policies)
}

// NodeSummaries constructs the bounded summary reader on the shared connection.
func (s *Stores) NodeSummaries(policies *searchaccess.PolicySet) *NodeSummaryStore {
	return NewNodeSummaryStore(s.db, policies)
}

// ServingSearchIndex reads the serving physical index that ops search
// provision recorded. It returns [searchdomain.ErrNoServingIndex] when none
// is recorded.
func (s *Stores) ServingSearchIndex(ctx context.Context) (index string, err error) {
	defer telemetry.FDBOp(ctx, "store.search_index.read")(&err)
	err = transactSession(ctx, s.db, s.Nodes.clock, "read serving search index", uuid.Nil, func(tr fdb.Transaction) error {
		value, readErr := tr.Get(fdb.Key(searchIndexKey())).Get()
		if readErr != nil {
			return sessionStepError{operation: "read serving search index", err: readErr}
		}
		index = string(value)
		return nil
	})
	if err != nil {
		return "", err
	}
	if index == "" {
		return "", searchdomain.ErrNoServingIndex
	}
	return index, nil
}
