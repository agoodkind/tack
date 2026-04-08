//go:build fdb

package foundationdb

// Stores bundles all FDB adapters that share one database connection.
type Stores struct {
	NodeTypes  *NodeTypeStore
	Properties *PropertyStore
	Activity   *ActivityStore
}

// NewStores opens FDB once and wires all adapters to the same connection.
func NewStores(clusterFile string) (*Stores, error) {
	db, err := Open(clusterFile)
	if err != nil {
		return nil, err
	}
	return &Stores{
		NodeTypes:  &NodeTypeStore{db: db},
		Properties: &PropertyStore{db: db},
		Activity:   &ActivityStore{db: db},
	}, nil
}
