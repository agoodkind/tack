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

// accessTypeDeclaration defines the node type fields the access policy reads.
type accessTypeDeclaration struct {
	TypeKey      string        `json:"type_key"`
	Features     node.Features `json:"features"`
	CanLiveUnder []string      `json:"can_live_under"`
	CanContain   []string      `json:"can_contain"`
}

// updateNodeTypeIndex updates the type-key index, the metadata epoch, and
// the projection digest in tr for one node type write. It clears the old
// type key's entry after a key change or delete. It sets the next type
// key's entry. When rescan is true, a change to the type key, features, or
// hierarchy declaration requests an access rescan of the organization.
// A feature change also requests content work for nodes of the type with
// a value under a definition that applies to an added or removed feature.
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
	if err := applyTypeProjectionChange(ctx, tr, orgID, old, next); err != nil {
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
	request := searchRescanRequest{
		AllContent: false, Names: nil, TypeKeys: typeKeysOf(old, next), Features: changedFeatures(old, next),
		Access: !bytes.Equal(oldDeclaration, newDeclaration), Projection: false,
	}
	if !rescan || !request.Access && len(request.Features) == 0 {
		return nil
	}
	return requestSearchRescan(ctx, tr, now, orgID, request)
}

// applyTypeProjectionChange updates the projection digest for one node type
// write when its contribution changed.
func applyTypeProjectionChange(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID, old, next *node.NodeType) error {
	change := projectionChange{previous: nil, next: nil, legacyPrevious: nil, legacyNext: nil, created: old == nil}
	var err error
	if change.previous, err = typeContribution(ctx, old); err != nil {
		return err
	}
	if change.next, err = typeContribution(ctx, next); err != nil {
		return err
	}
	if bytes.Equal(change.previous, change.next) {
		return nil
	}
	return applyProjectionChange(ctx, tr, orgID, change)
}

// typeKeysOf returns the distinct nonempty type keys of one node type write.
func typeKeysOf(old, next *node.NodeType) []string {
	keys := make([]string, 0, 2)
	for _, kind := range []*node.NodeType{old, next} {
		if kind != nil && kind.TypeKey != "" && !slices.Contains(keys, kind.TypeKey) {
			keys = append(keys, kind.TypeKey)
		}
	}
	return keys
}

// changedFeatures returns the features that exactly one of old and next
// declares.
func changedFeatures(old, next *node.NodeType) []string {
	var before, after node.Features
	if old != nil {
		before = old.Features
	}
	if next != nil {
		after = next.Features
	}
	changed := make([]string, 0, len(before)+len(after))
	for _, feature := range before {
		if !after.Has(feature) && !slices.Contains(changed, feature) {
			changed = append(changed, feature)
		}
	}
	for _, feature := range after {
		if !before.Has(feature) && !slices.Contains(changed, feature) {
			changed = append(changed, feature)
		}
	}
	return changed
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
