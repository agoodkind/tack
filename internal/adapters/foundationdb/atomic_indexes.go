package foundationdb

import (
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
)

func writeAtomicIndexes(
	transaction fdb.Transaction,
	current *node.Node,
	oldProps map[string]json.RawMessage,
	indexedProps []string,
	referenceKeys []node.ReferenceKey,
) error {
	reconcilePropertyIndexes(transaction, current, oldProps, indexedProps)
	if referenceKeys != nil {
		return writeReferenceKeys(transaction, current.OrgID, current.ID, referenceKeys)
	}

	return nil
}
