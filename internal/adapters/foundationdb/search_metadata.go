package foundationdb

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// updatePropertyDefinition updates the property-name index, the metadata
// epoch, and the projection digest in tr for one definition write. previous
// is the stored definition JSON, and next is nil for a delete. Any stored
// change increments the epoch. When rescan is true, a changed identity,
// name, or declaration requests a content rescan of the nodes with a value
// under the old or new property name.
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
	if err := bumpMetadataEpoch(ctx, tr, orgID, previous, next); err != nil {
		return err
	}
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
	return requestSearchRescan(ctx, tr, now, orgID, affectedPropertyNames(old, next), false)
}

// affectedPropertyNames returns the distinct old and new names of one
// definition write. A node with a value under either name emits changed text.
func affectedPropertyNames(old, next *node.PropertyDef) []string {
	names := make([]string, 0, 2)
	for _, definition := range []*node.PropertyDef{old, next} {
		if definition != nil && !slices.Contains(names, definition.Name) {
			names = append(names, definition.Name)
		}
	}
	return names
}

// bumpMetadataEpoch increments the organization's metadata epoch when the
// stored JSON of one definition or node type changes.
func bumpMetadataEpoch[Record node.PropertyDef | node.NodeType](ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, previous []byte, next *Record) error {
	var encoded []byte
	if next != nil {
		var err error
		encoded, err = json.Marshal(next)
		if err != nil {
			return searchReadFailure(ctx, "encode metadata record", err)
		}
	}
	if bytes.Equal(previous, encoded) {
		return nil
	}
	_, err := incrementSearchCounter(ctx, tr, searchEpochKey(orgID))
	return err
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
	if err := bumpMetadataEpoch(ctx, tr, orgID, previous, next); err != nil {
		return err
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
	return requestSearchRescan(ctx, tr, now, orgID, nil, true)
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
