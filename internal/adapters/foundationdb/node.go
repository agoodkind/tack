package foundationdb

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// NodeStore implements node.NodeRepository using FoundationDB.
type NodeStore struct {
	db fdb.Database
}

func NewNodeStore(db fdb.Database) *NodeStore {
	return &NodeStore{db: db}
}

// Get resolves the node by ID. It reads the resolve record (not org-scoped) to
// discover the NodeType, then reads the primary record.
func (s *NodeStore) Get(_ context.Context, orgID, nodeID uuid.UUID) (*node.Node, error) {
	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		resolveBytes, err := tr.Get(fdb.Key(nodeResolveKey(nodeID))).Get()
		if err != nil || len(resolveBytes) == 0 {
			return nil, err
		}
		var resolve node.NodeResolve
		if err := json.Unmarshal(resolveBytes, &resolve); err != nil {
			return nil, fmt.Errorf("unmarshal resolve: %w", err)
		}
		if resolve.OrgID != orgID {
			return nil, nil // wrong org
		}
		return tr.Get(fdb.Key(nodeInstanceKey(orgID, resolve.NodeType, nodeID))).Get()
	})
	if err != nil {
		return nil, fmt.Errorf("fdb get node: %w", err)
	}
	b, ok := val.([]byte)
	if !ok || len(b) == 0 {
		return nil, nil
	}
	var n node.Node
	if err := json.Unmarshal(b, &n); err != nil {
		return nil, fmt.Errorf("unmarshal node: %w", err)
	}
	return &n, nil
}

// Set overwrites an existing node's primary record and view. Property index
// entries are NOT refreshed by Set; callers changing indexed property values
// must delete the old index entries and add new ones via the atomic update
// helper (to be added when the service layer needs it). For the MVP, Set is
// used for rename and Props updates in cases where no indexed value changed.
func (s *NodeStore) Set(_ context.Context, n *node.Node, view *node.NodeView) error {
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		nBytes, err := json.Marshal(n)
		if err != nil {
			return nil, fmt.Errorf("marshal node: %w", err)
		}
		tr.Set(fdb.Key(nodeInstanceKey(n.OrgID, n.NodeType, n.ID)), nBytes)

		resolveBytes, err := json.Marshal(&node.NodeResolve{
			OrgID:    n.OrgID,
			NodeType: n.NodeType,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal resolve: %w", err)
		}
		tr.Set(fdb.Key(nodeResolveKey(n.ID)), resolveBytes)

		if view != nil {
			viewBytes, err := json.Marshal(view)
			if err != nil {
				return nil, fmt.Errorf("marshal view: %w", err)
			}
			tr.Set(fdb.Key(nodeViewKey(n.OrgID, n.NodeType, n.ID)), viewBytes)
		}
		return nil, nil
	})
	return err
}

// Delete removes the primary node, view, resolve, property indexes, and all
// relationships where the node is source or target. Uses range clears for the
// per-node relationship prefixes; no per-relationship scan required.
func (s *NodeStore) Delete(ctx context.Context, orgID, nodeID uuid.UUID) error {
	// First discover NodeType via resolve so we can construct the instance and
	// view keys correctly.
	n, err := s.Get(ctx, orgID, nodeID)
	if err != nil {
		return err
	}
	if n == nil {
		return nil
	}

	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		// Clear the primary, view, and resolve records.
		tr.Clear(fdb.Key(nodeInstanceKey(orgID, n.NodeType, nodeID)))
		tr.Clear(fdb.Key(nodeViewKey(orgID, n.NodeType, nodeID)))
		tr.Clear(fdb.Key(nodeResolveKey(nodeID)))

		// Clear all relationships where this node is source. The target-side
		// reverse entries need to be cleared too; we range-read the source side
		// to know the (relationType, targetID) pairs.
		fwdPrefix := relationshipPrefixBySource(orgID, nodeID, "")
		fwdRange, err := fdb.PrefixRange(fwdPrefix)
		if err != nil {
			return nil, err
		}
		fwdKVs := tr.GetRange(fwdRange, fdb.RangeOptions{}).GetSliceOrPanic()
		for _, kv := range fwdKVs {
			t, terr := tuple.Unpack(kv.Key)
			if terr != nil || len(t) < 5 {
				continue
			}
			relType, _ := t[3].(string)
			targetStr, _ := t[4].(string)
			targetID, perr := uuid.Parse(targetStr)
			if perr != nil {
				continue
			}
			tr.Clear(fdb.Key(relationshipReverseKey(orgID, targetID, relType, nodeID)))
		}
		tr.ClearRange(fwdRange)

		// Clear all relationships where this node is target, plus their forward
		// counterparts.
		revPrefix := relationshipReversePrefixByTarget(orgID, nodeID, "")
		revRange, err := fdb.PrefixRange(revPrefix)
		if err != nil {
			return nil, err
		}
		revKVs := tr.GetRange(revRange, fdb.RangeOptions{}).GetSliceOrPanic()
		for _, kv := range revKVs {
			t, terr := tuple.Unpack(kv.Key)
			if terr != nil || len(t) < 5 {
				continue
			}
			relType, _ := t[3].(string)
			sourceStr, _ := t[4].(string)
			sourceID, perr := uuid.Parse(sourceStr)
			if perr != nil {
				continue
			}
			tr.Clear(fdb.Key(relationshipKey(orgID, sourceID, relType, nodeID)))
		}
		tr.ClearRange(revRange)

		// Clear property index entries. Secondary index is keyed by
		// (orgID, nodeType, propName, encodedValue, nodeID); we don't have a
		// per-node reverse index, so we walk the Props we know about.
		for propName, raw := range n.Props {
			encoded := encodePropertyValue(raw)
			tr.Clear(fdb.Key(nodeByPropertyKey(orgID, n.NodeType, propName, encoded, nodeID)))
		}

		return nil, nil
	})
	return err
}

// CreateAtomic writes a new node plus initial relationships in one FDB
// transaction. indexedProps names the Props keys that should receive a
// property-index entry; the caller resolves this from the PropertyDef registry.
func (s *NodeStore) CreateAtomic(
	_ context.Context,
	n *node.Node,
	view *node.NodeView,
	rels []*node.Relationship,
	indexedProps []string,
) error {
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		// 1. Primary node record.
		nBytes, err := json.Marshal(n)
		if err != nil {
			return nil, fmt.Errorf("marshal node: %w", err)
		}
		tr.Set(fdb.Key(nodeInstanceKey(n.OrgID, n.NodeType, n.ID)), nBytes)

		// 2. Global resolve.
		resolveBytes, err := json.Marshal(&node.NodeResolve{
			OrgID:    n.OrgID,
			NodeType: n.NodeType,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal resolve: %w", err)
		}
		tr.Set(fdb.Key(nodeResolveKey(n.ID)), resolveBytes)

		// 3. Materialized view.
		if view != nil {
			viewBytes, err := json.Marshal(view)
			if err != nil {
				return nil, fmt.Errorf("marshal view: %w", err)
			}
			tr.Set(fdb.Key(nodeViewKey(n.OrgID, n.NodeType, n.ID)), viewBytes)
		}

		// 4. Property secondary indexes for indexed Props only.
		for _, propName := range indexedProps {
			raw, ok := n.Props[propName]
			if !ok || len(raw) == 0 {
				continue
			}
			tr.Set(fdb.Key(nodeByPropertyKey(n.OrgID, n.NodeType, propName, encodePropertyValue(raw), n.ID)), []byte{})
		}

		// 5. Relationships (forward + reverse).
		for _, rel := range rels {
			if rel.OrgID == uuid.Nil {
				rel.OrgID = n.OrgID
			}
			metadata, err := json.Marshal(rel)
			if err != nil {
				return nil, fmt.Errorf("marshal relationship: %w", err)
			}
			tr.Set(fdb.Key(relationshipKey(rel.OrgID, rel.SourceID, rel.RelationType, rel.TargetID)), metadata)
			tr.Set(fdb.Key(relationshipReverseKey(rel.OrgID, rel.TargetID, rel.RelationType, rel.SourceID)), []byte{})
		}
		return nil, nil
	})
	return err
}

// ListByProperty scans the secondary index for (orgID, nodeType, propName)
// narrowed to the given value, and returns the matching Node records.
func (s *NodeStore) ListByProperty(
	_ context.Context,
	orgID uuid.UUID,
	nodeType, propName string,
	value json.RawMessage,
) ([]*node.Node, error) {
	pr, err := fdb.PrefixRange(nodeByPropertyValuePrefix(orgID, nodeType, propName, encodePropertyValue(value)))
	if err != nil {
		return nil, err
	}
	vals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		indexKVs, err := tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
		if err != nil {
			return nil, err
		}
		nodes := make([]*node.Node, 0, len(indexKVs))
		for _, kv := range indexKVs {
			t, terr := tuple.Unpack(kv.Key)
			if terr != nil || len(t) < 6 {
				continue
			}
			nodeIDStr, _ := t[5].(string)
			id, perr := uuid.Parse(nodeIDStr)
			if perr != nil {
				continue
			}
			instance, err := tr.Get(fdb.Key(nodeInstanceKey(orgID, nodeType, id))).Get()
			if err != nil || len(instance) == 0 {
				continue
			}
			var n node.Node
			if err := json.Unmarshal(instance, &n); err != nil {
				continue
			}
			nodes = append(nodes, &n)
		}
		return nodes, nil
	})
	if err != nil {
		return nil, fmt.Errorf("fdb list by property: %w", err)
	}
	return vals.([]*node.Node), nil
}

// AllocateSequence atomically increments and returns the next sequence number
// for (orgID, scopeNodeID, nodeType).
func (s *NodeStore) AllocateSequence(_ context.Context, orgID, scopeNodeID uuid.UUID, nodeType string) (int64, error) {
	val, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		k := fdb.Key(sequenceKey(orgID, scopeNodeID, nodeType))
		b, err := tr.Get(k).Get()
		if err != nil {
			return int64(0), err
		}
		var current int64
		if len(b) >= 8 {
			current = int64(binary.LittleEndian.Uint64(b))
		}
		next := current + 1
		buf := make([]byte, 8)
		binary.LittleEndian.PutUint64(buf, uint64(next))
		tr.Set(k, buf)
		return next, nil
	})
	if err != nil {
		return 0, fmt.Errorf("fdb allocate sequence: %w", err)
	}
	return val.(int64), nil
}

// GetSlug returns the nodeID registered under (nodeType, slug), or uuid.Nil, nil
// when none exists.
func (s *NodeStore) GetSlug(_ context.Context, nodeType, slug string) (uuid.UUID, error) {
	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(fdb.Key(slugIndexKey(nodeType, slug))).Get()
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("fdb get slug: %w", err)
	}
	b, ok := val.([]byte)
	if !ok || len(b) == 0 {
		return uuid.Nil, nil
	}
	return uuid.FromBytes(b)
}

func (s *NodeStore) WriteSlug(_ context.Context, nodeType, slug string, nodeID uuid.UUID) error {
	idBytes, _ := nodeID.MarshalBinary()
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(fdb.Key(slugIndexKey(nodeType, slug)), idBytes)
		return nil, nil
	})
	return err
}

func (s *NodeStore) DeleteSlug(_ context.Context, nodeType, slug string) error {
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Clear(fdb.Key(slugIndexKey(nodeType, slug)))
		return nil, nil
	})
	return err
}
