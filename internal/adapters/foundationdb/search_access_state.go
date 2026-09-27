package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/telemetry"
)

// SearchAccessStateStore compiles current access with the write versions
// recorded for each node.
type SearchAccessStateStore struct {
	db       fdb.Database
	policies *searchaccess.PolicySet
}

// NewSearchAccessStateStore creates the FoundationDB access-state adapter.
func NewSearchAccessStateStore(db fdb.Database, policies *searchaccess.PolicySet) *SearchAccessStateStore {
	return &SearchAccessStateStore{db: db, policies: policies}
}

// Compile compiles the node's access at the work generation with the write
// versions of the node's authority rollout. The work store compares it with
// the recorded access. A rollout that adds or removes a write version
// changes the compiled access. Access work then updates every page.
func (s *SearchAccessStateStore) Compile(ctx context.Context, work searchdomain.Work) (access node.SearchAccess, err error) {
	defer telemetry.FDBOp(ctx, "store.search_access.compile")(&err)
	var rollout searchRolloutRecord
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		var readErr error
		rollout, readErr = readRollout(ctx, tr, work.OrgID)
		return readErr
	})
	if err != nil {
		return access, searchStorageError(ctx, "search.access_state.rollout_failed", "read access write versions", work.NodeID, err)
	}
	access, err = compileSearchAccess(ctx, s.policies, rollout.WriteVersions, work.OrgID, work.NodeID, work.Generation)
	if err != nil {
		return access, nodeContentContextError{operation: "compile current search access for node " + work.NodeID.String(), err: err}
	}
	return access, nil
}

// Dependents reads one bounded page of the nodes that need access work
// after the work node changed. For a deleted node, Dependents reads the
// recorded counterparts of that node. For any other node, Dependents reads
// the policy dependents under every recorded write version after the work
// cursor.
func (s *SearchAccessStateStore) Dependents(ctx context.Context, work searchdomain.Work, limit int) (node.IDPage, error) {
	if work.Deleted {
		return s.readDeletedFanout(ctx, work, limit)
	}
	recorded, err := s.recordedAccess(ctx, work)
	if err != nil {
		return node.IDPage{}, err
	}
	page, err := s.policies.Dependents(ctx, recorded.Versions, work.OrgID, work.NodeID, work.Cursor, limit)
	if err != nil {
		return node.IDPage{}, nodeContentContextError{operation: "list search access dependents of node " + work.NodeID.String(), err: err}
	}
	return page, nil
}

func (s *SearchAccessStateStore) recordedAccess(ctx context.Context, work searchdomain.Work) (node.SearchAccess, error) {
	var recorded node.SearchAccess
	err := transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		var readErr error
		recorded, readErr = accessStateFor(ctx, tr, work.OrgID, work.NodeID)
		return readErr
	})
	if err != nil {
		return recorded, searchStorageError(ctx, "search.access_state.read_failed", "read search access state", work.NodeID, err)
	}
	return recorded, nil
}
