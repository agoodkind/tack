package foundationdb

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// updatePropertyDefinition updates the property-name index and the
// projection digest in tr for one definition write. previous is the stored
// definition JSON, and next is nil for a delete. When rescan is true, a
// changed identity, name, or declaration requests a content rescan of the
// organization.
func updatePropertyDefinition(ctx context.Context, tr fdb.Transaction, now time.Time, rescan bool, orgID, definitionID uuid.UUID, previous []byte, next *node.PropertyDef) error {
	var old *node.PropertyDef
	if len(previous) > 0 {
		var decoded node.PropertyDef
		if err := json.Unmarshal(previous, &decoded); err != nil {
			return searchReadFailure(ctx, "decode stored property definition "+definitionID.String(), err)
		}
		old = &decoded
	}
	indexPropertyName(tr, orgID, definitionID, old, next)
	oldHash, err := declarationHash(ctx, old)
	if err != nil {
		return err
	}
	newHash, err := declarationHash(ctx, next)
	if err != nil {
		return err
	}
	if bytes.Equal(oldHash, newHash) {
		return nil
	}
	if err := updateProjectionDigest(ctx, tr, orgID, oldHash, newHash); err != nil {
		return err
	}
	if !rescan {
		return nil
	}
	return requestSearchRescan(ctx, tr, now, orgID, true, false)
}

// indexPropertyName updates the property-name index in tr for one definition
// write. It clears the entry of the old name after a rename or delete and
// sets the entry of the next name. old is the stored definition or nil, and
// next is nil for a delete.
func indexPropertyName(tr fdb.Transaction, orgID, definitionID uuid.UUID, old, next *node.PropertyDef) {
	if old != nil && (next == nil || old.Name != next.Name) {
		tr.Clear(fdb.Key(propertyDefByNameKey(orgID, old.Name, definitionID)))
	}
	if next != nil {
		tr.Set(fdb.Key(propertyDefByNameKey(orgID, next.Name, definitionID)), []byte{})
	}
}

// accessTypeDeclaration is the part of a node type the access policy reads.
type accessTypeDeclaration struct {
	TypeKey      string        `json:"type_key"`
	Features     node.Features `json:"features"`
	CanLiveUnder []string      `json:"can_live_under"`
	CanContain   []string      `json:"can_contain"`
}

// updateNodeTypeIndex updates the type-key index in tr for one node type
// write. It clears the entry of the old type key after a key change or
// delete and sets the entry of the next type key. When rescan is true, a
// changed type key, feature set, or hierarchy declaration requests an access
// rescan of the organization. A node type write requests no content work
// because projected text reads no node type field.
func updateNodeTypeIndex(ctx context.Context, tr fdb.Transaction, now time.Time, rescan bool, orgID, typeID uuid.UUID, previous []byte, next *node.NodeType) error {
	var old *node.NodeType
	if len(previous) > 0 {
		var decoded node.NodeType
		if err := json.Unmarshal(previous, &decoded); err != nil {
			return searchReadFailure(ctx, "decode stored node type "+typeID.String(), err)
		}
		old = &decoded
	}
	if old != nil && old.TypeKey != "" && (next == nil || old.TypeKey != next.TypeKey) {
		tr.Clear(fdb.Key(nodeTypeByKeyKey(orgID, old.TypeKey, typeID)))
	}
	if next != nil && next.TypeKey != "" {
		tr.Set(fdb.Key(nodeTypeByKeyKey(orgID, next.TypeKey, typeID)), []byte{})
	}
	oldDeclaration, err := encodeAccessType(ctx, old)
	if err != nil {
		return err
	}
	newDeclaration, err := encodeAccessType(ctx, next)
	if err != nil {
		return err
	}
	if !rescan || bytes.Equal(oldDeclaration, newDeclaration) {
		return nil
	}
	return requestSearchRescan(ctx, tr, now, orgID, false, true)
}

func encodeAccessType(ctx context.Context, kind *node.NodeType) ([]byte, error) {
	if kind == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(accessTypeDeclaration{TypeKey: kind.TypeKey, Features: kind.Features, CanLiveUnder: kind.CanLiveUnder, CanContain: kind.CanContain})
	if err != nil {
		return nil, searchReadFailure(ctx, "encode node type access declaration "+kind.ID.String(), err)
	}
	return encoded, nil
}
