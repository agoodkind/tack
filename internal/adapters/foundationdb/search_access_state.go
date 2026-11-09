package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/telemetry"
)

// SearchAccessStateStore compiles current access using the authority
// rollout's write versions and reads recorded access for dependent queries.
type SearchAccessStateStore struct {
	db       fdb.Database
	policies *searchaccess.PolicySet
}

// NewSearchAccessStateStore creates the FoundationDB access state adapter.
//
// The adapter uses db to read rollout and recorded access state. It uses
// policies for access compilation and dependent queries.
//
//   - Compile supplies each work item's generation to the policy compiler.
func NewSearchAccessStateStore(db fdb.Database, policies *searchaccess.PolicySet) *SearchAccessStateStore {
	return &SearchAccessStateStore{db: db, policies: policies}
}

// Compile compiles the node's access at the work generation with the write
// versions of its authority rollout.
//
// The work store compares compiled versions and keys with recorded access. A
// rollout that adds or removes a write version changes compiled access.
// Access work rewrites every page unless tracking confirms that all issued
// pages already store that access.
//
//   - Compilation merges opaque keys from every write version.
//   - Compilation sorts and removes duplicate versions and keys.
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

// Dependents reads one bounded page of nodes that need access work after the
// work node changes.
//
// A deleted node uses recorded counterparts. Other nodes use policy
// dependents under the recorded write versions after the work cursor.
//
//   - A policy cursor records both the version position and the compiler
//     cursor.
//   - Deleted node queries start at the first remaining counterpart record.
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
