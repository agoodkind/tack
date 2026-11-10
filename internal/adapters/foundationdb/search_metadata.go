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
// epoch, and the projection digests in tr for one definition write.
// previous stores the definition's JSON before the write. next is nil for
// a delete. Any change to the stored record increments the epoch. When
// rescan is true, a change to a rendering input requests a content rescan
// for nodes with a value under the old or new property name.
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
	change, err := definitionProjectionChange(ctx, old, next)
	if err != nil {
		return err
	}
	if bytes.Equal(change.previous, change.next) && bytes.Equal(change.legacyPrevious, change.legacyNext) {
		return nil
	}
	if err := applyProjectionChange(ctx, tr, orgID, change); err != nil {
		return err
	}
	if !rescan {
		return nil
	}
	request := searchRescanRequest{
		AllContent: false, Names: affectedPropertyNames(old, next), TypeKeys: nil, Features: nil,
		Access: false, Projection: false,
	}
	return requestSearchRescan(ctx, tr, now, orgID, request)
}

// definitionProjectionChange returns the current format's contributions
// and the legacy contributions for one definition write.
func definitionProjectionChange(ctx context.Context, old, next *node.PropertyDef) (projectionChange, error) {
	change := projectionChange{previous: nil, next: nil, legacyPrevious: nil, legacyNext: nil, created: old == nil}
	var err error
	if change.previous, err = definitionContribution(ctx, old); err != nil {
		return change, err
	}
	if change.next, err = definitionContribution(ctx, next); err != nil {
		return change, err
	}
	if change.legacyPrevious, err = declarationHash(ctx, old); err != nil {
		return change, err
	}
	change.legacyNext, err = declarationHash(ctx, next)
	return change, err
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
