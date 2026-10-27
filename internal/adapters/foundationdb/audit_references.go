package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/domain/node"
)

// referenceClaims stores the references that one node owns in the reference_keys member.
// ReferenceKeys maps each reference template name to its rendered reference.
type referenceClaims struct {
	ReferenceKeys map[string]string `json:"reference_keys"`
}

// readOwnedReferences reads the references that nodeID owns inside tr.
func readOwnedReferences(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) (map[string]string, error) {
	keyRange, err := fdb.PrefixRange(nodeReferenceOwnedPrefix(orgID, nodeID))
	if err != nil {
		return nil, nodeOperationFailure(ctx, "create the reference range of node "+nodeID.String(), err)
	}
	items, err := tr.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read the references of node "+nodeID.String(), err)
	}
	owned := make(map[string]string, len(items))
	for _, item := range items {
		fields, err := tuple.Unpack(stripPrefix(item.Key))
		if err != nil || len(fields) != 4 {
			return nil, nodeOperationFailure(ctx, "decode a reference of node "+nodeID.String(), fmt.Errorf("reverse reference entry %x is malformed", item.Key))
		}
		templateName, ok := fields[3].(string)
		if !ok {
			return nil, nodeOperationFailure(ctx, "decode a reference of node "+nodeID.String(), fmt.Errorf("reverse reference entry %x has a non-string template name", item.Key))
		}
		owned[templateName] = string(item.Value)
	}
	return owned, nil
}

// recordReferenceChange records the change of the references that nodeID
// owns as a change of the node target. The old and new values contain only
// the reference_keys member.
func recordReferenceChange(
	ctx context.Context, tr fdb.Transaction, changes *mutationAudit, orgID, nodeID uuid.UUID,
	before map[string]string, keys []node.ReferenceKey,
) error {
	after := make(map[string]string, len(keys))
	for _, key := range keys {
		after[key.TemplateName] = key.Encoded
	}
	resolveBytes, err := tr.Get(fdb.Key(nodeResolveKey(nodeID))).Get()
	if err != nil {
		return searchReadFailure(ctx, "read the resolution record of node "+nodeID.String(), err)
	}
	var resolve node.NodeResolve
	if err := json.Unmarshal(resolveBytes, &resolve); err != nil {
		return nodeOperationFailure(ctx, "decode the resolution record of node "+nodeID.String(), err)
	}
	if resolve.OrgID != orgID {
		return nodeOperationFailure(ctx, "resolve node "+nodeID.String(), fmt.Errorf("the node does not belong to org %s", orgID))
	}
	beforeJSON, err := json.Marshal(referenceClaims{ReferenceKeys: before})
	if err != nil {
		return nodeOperationFailure(ctx, "encode the references of node "+nodeID.String(), err)
	}
	afterJSON, err := json.Marshal(referenceClaims{ReferenceKeys: after})
	if err != nil {
		return nodeOperationFailure(ctx, "encode the references of node "+nodeID.String(), err)
	}
	if err := changes.record(nodeIdentityTarget(nodeID, resolve.NodeType, ""), beforeJSON, afterJSON); err != nil {
		return nodeOperationFailure(ctx, "record the references of node "+nodeID.String(), err)
	}
	return nil
}
