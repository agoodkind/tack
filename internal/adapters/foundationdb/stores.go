//go:build fdb

// Package foundationdb provides FoundationDB adapters for all FDB-backed stores:
// assignments, labels, activity, membership, containment, and node cleanup.
package foundationdb

import (
	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Stores bundles all FDB adapters sharing one database connection.
type Stores struct {
	NodeTypes   *NodeTypeStore
	Properties  *PropertyStore
	Activity    *ActivityStore
	Assignments *AssignmentStore
	Labels      *NodeLabelStore
	Membership  *MembershipStore
	Containment *ContainmentStore
	NodeDeleter *NodeDeleteStore
	Entities    *EntityStore
	Automations *AutomationStore
	Views       *ViewStore
	Comments    *CommentStore
	Org         *OrgFDBStore
	Workspace   *WorkspaceFDBStore
	Project     *ProjectFDBStore
	State       *StateFDBStore
	Label       *LabelFDBStore
}

// NewStores opens FDB once and wires all adapters to the same connection.
// sqlPool is required for adapters that need SQL (e.g., workspace list operations).
func NewStores(clusterFile string, sqlPool *pgxpool.Pool) (*Stores, error) {
	db, err := Open(clusterFile)
	if err != nil {
		return nil, err
	}
	return newStores(db, sqlPool), nil
}

func newStores(db fdb.Database, sqlPool *pgxpool.Pool) *Stores {
	return &Stores{
		NodeTypes:   &NodeTypeStore{db: db},
		Properties:  &PropertyStore{db: db},
		Activity:    &ActivityStore{db: db},
		Assignments: newAssignmentStore(db),
		Labels:      newNodeLabelStore(db),
		Membership:  newMembershipStore(db),
		Containment: newContainmentStore(db),
		NodeDeleter: newNodeDeleteStore(db),
		Entities:    NewEntityStore(db),
		Automations: NewAutomationStore(db),
		Views:       NewViewStore(db),
		Comments:    NewCommentStore(db),
		Org:         NewOrgFDBStore(db),
		Workspace:   NewWorkspaceFDBStore(db, sqlPool),
		Project:     NewProjectFDBStore(db, NewEntityStore(db), NewViewStore(db)),
		State:       NewStateFDBStore(db, NewEntityStore(db), NewViewStore(db)),
		Label:       NewLabelFDBStore(db, NewEntityStore(db), NewViewStore(db)),
	}
}
