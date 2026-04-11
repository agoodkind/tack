//go:build fdb

// Package foundationdb provides FoundationDB adapters for all FDB-backed stores:
// assignments, labels, activity, membership, containment, and node cleanup.
package foundationdb

import "github.com/apple/foundationdb/bindings/go/src/fdb"

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
}

// NewStores opens FDB once and wires all adapters to the same connection.
func NewStores(clusterFile string) (*Stores, error) {
	db, err := Open(clusterFile)
	if err != nil {
		return nil, err
	}
	return newStores(db), nil
}

func newStores(db fdb.Database) *Stores {
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
	}
}
