// Package foundationdb provides FoundationDB adapters for the generic node,
// property, and relationship stores. No concept-specific stores exist:
// assignments, labels, comments, activity, automation etc. are all expressed
// as Nodes + Relationships in the generic primitives.
package foundationdb

import (
	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Stores bundles the generic FDB adapters.
type Stores struct {
	NodeTypes     *NodeTypeStore
	PropertyDefs  *PropertyDefStore
	Nodes         *NodeStore
	Views         *ViewStore
	Relationships *RelationshipStore
	Inspect       *InspectStore
	NodeDeleter   *NodeDeleteStore
}

// NewStores opens FDB once and wires all generic stores to the same connection.
// sqlPool is reserved for auth-adjacent queries (org_members), not domain data.
func NewStores(clusterFile string, sqlPool *pgxpool.Pool) (*Stores, error) {
	db, err := Open(clusterFile)
	if err != nil {
		return nil, err
	}
	return newStores(db, sqlPool), nil
}

func newStores(db fdb.Database, _ *pgxpool.Pool) *Stores {
	return &Stores{
		NodeTypes:     NewNodeTypeStore(db),
		PropertyDefs:  NewPropertyDefStore(db),
		Nodes:         NewNodeStore(db),
		Views:         NewViewStore(db),
		Relationships: NewRelationshipStore(db),
		Inspect:       NewInspectStore(db),
		NodeDeleter:   NewNodeDeleteStore(db),
	}
}
