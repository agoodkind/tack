package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

// NodeDeleteStore implements node.NodeDeleter.
// It cleans up all FDB key spaces scoped to a node_id when the node is deleted.
//
// Design notes:
//   - Keys where (orgID, nodeID) is an early prefix are range-cleared in one transaction.
//   - assignment_to_user and issues_with_label reverse indexes are cleaned by reading
//     the forward index first (assignees/labels are known) and then deleting exactly.
//   - Keys where nodeID is NOT the primary scope (draft_for_user_on_node,
//     sort_position_in_view, activity_by_user, etc.) are left as low-priority orphans
//     and are handled by a GC pass rather than per-delete cleanup.
type NodeDeleteStore struct{ db fdb.Database }

func newNodeDeleteStore(db fdb.Database) *NodeDeleteStore { return &NodeDeleteStore{db: db} }

// DeleteNode removes all FDB data for the given node.
func (s *NodeDeleteStore) DeleteNode(_ context.Context, orgID, nodeID uuid.UUID) error {
	org := orgID.String()
	nid := nodeID.String()

	// Step 1: read current assignees and labels so we can clean reverse indexes.
	type forwardData struct {
		assignees []string // userID strings
		labels    []string // labelID strings
	}
	fd, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		var d forwardData
		aPr, _ := fdb.PrefixRange(tuple.Tuple{keyAssignmentOnNode, org, nid}.Pack())
		aKVs, _ := tr.GetRange(aPr, fdb.RangeOptions{}).GetSliceWithError()
		for _, kv := range aKVs {
			t, err := tuple.Unpack(kv.Key)
			if err != nil || len(t) < 4 {
				continue
			}
			if s, ok := t[3].(string); ok {
				d.assignees = append(d.assignees, s)
			}
		}
		lPr, _ := fdb.PrefixRange(tuple.Tuple{keyLabelOnNode, org, nid}.Pack())
		lKVs, _ := tr.GetRange(lPr, fdb.RangeOptions{}).GetSliceWithError()
		for _, kv := range lKVs {
			t, err := tuple.Unpack(kv.Key)
			if err != nil || len(t) < 4 {
				continue
			}
			if s, ok := t[3].(string); ok {
				d.labels = append(d.labels, s)
			}
		}
		return d, nil
	})
	if err != nil {
		return err
	}
	fwd := fd.(forwardData)

	// Step 2: clear all node-scoped FDB data in a single write transaction.
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		// Helper: range-clear by (prefix, org, nid) prefix.
		clearNodeRange := func(prefix string) {
			pr, _ := fdb.PrefixRange(tuple.Tuple{prefix, org, nid}.Pack())
			tr.ClearRange(pr)
		}

		// Simple forward-only range clears.
		clearNodeRange(keyCommentOnNode)
		clearNodeRange(keyActivityOnNode)
		clearNodeRange(keyCountOnNode)
		clearNodeRange(keyLinkOnNode)
		clearNodeRange(keyAttachmentOnNode)
		clearNodeRange(keyDescriptionVersion)
		clearNodeRange(keyWorkLogOnNode)
		clearNodeRange(keyPropertyValueOnNode)
		clearNodeRange(keyPresenceOnNode)
		clearNodeRange(keyReactionOnNode)
		clearNodeRange(keyWatcherOfNode)    // reverse (node_watched_by_user) left as GC
		clearNodeRange(keyMentionInNode)
		clearNodeRange(keyRelationFromNode) // outgoing — reverse (relation_to_node) left as GC
		clearNodeRange(keyRelationToNode)   // incoming — forward (relation_from_node) left as GC

		// Assignments: clear forward range + scan each user's reverse entries.
		clearNodeRange(keyAssignmentOnNode)
		for _, userID := range fwd.assignees {
			uPr, _ := fdb.PrefixRange(tuple.Tuple{keyAssignmentToUser, org, userID}.Pack())
			uKVs, err := tr.GetRange(uPr, fdb.RangeOptions{}).GetSliceWithError()
			if err != nil {
				continue
			}
			for _, kv := range uKVs {
				t, err := tuple.Unpack(kv.Key)
				// key: (keyAssignmentToUser, org, userID, timestampNano, nodeID)
				if err != nil || len(t) < 5 {
					continue
				}
				if last, ok := t[len(t)-1].(string); ok && last == nid {
					tr.Clear(kv.Key)
				}
			}
		}

		// Labels: clear forward range + delete each reverse entry directly.
		clearNodeRange(keyLabelOnNode)
		for _, labelID := range fwd.labels {
			tr.Clear(fdb.Key(tuple.Tuple{keyNodesWithLabel, org, labelID, nid}.Pack()))
		}

		return nil, nil
	})
	return err
}
