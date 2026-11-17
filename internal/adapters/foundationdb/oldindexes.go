package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
)

const parentIDProperty = "parent_id"

// moveParentIndexEntry replaces an existing parent_id index entry of current.
// It does not create an index entry when the old value is absent.
func moveParentIndexEntry(
	ctx context.Context,
	transaction fdb.Transaction,
	current *node.Node,
	oldValue json.RawMessage,
) error {
	if len(oldValue) == 0 {
		return nil
	}

	oldKey := fdb.Key(
		nodeByPropertyKey(
			current.OrgID,
			current.NodeType,
			parentIDProperty,
			encodePropertyValue(oldValue),
			current.ID,
		),
	)
	existing, err := transaction.Get(oldKey).Get()
	if err != nil {
		return searchReadFailure(
			ctx,
			"read parent_id index entry of node "+current.ID.String(),
			err,
		)
	}

	if existing == nil {
		return nil
	}

	transaction.Clear(oldKey)
	newValue := current.Props[parentIDProperty]
	transaction.Set(
		fdb.Key(
			nodeByPropertyKey(
				current.OrgID,
				current.NodeType,
				parentIDProperty,
				encodePropertyValue(newValue),
				current.ID,
			),
		),
		[]byte{},
	)

	return nil
}

// setProperty returns props with name set to value. A nil map becomes a new
// map.
func setProperty(
	props map[string]json.RawMessage,
	name string,
	value json.RawMessage,
) map[string]json.RawMessage {
	if props == nil {
		props = make(map[string]json.RawMessage, 1)
	}

	props[name] = value

	return props
}
