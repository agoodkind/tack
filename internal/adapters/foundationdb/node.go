package foundationdb

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
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
func (s *NodeStore) Get(ctx context.Context, orgID, nodeID uuid.UUID) (n *node.Node, err error) {
	defer telemetry.FDBOp(ctx, "store.node.get")(&err)
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
	var nv node.Node
	if err := json.Unmarshal(b, &nv); err != nil {
		return nil, fmt.Errorf("unmarshal node: %w", err)
	}
	return &nv, nil
}

// Set overwrites an existing node's primary record and view. Property index
// entries are NOT refreshed by Set. Callers that change an indexed property
// value must use UpdateAtomic so the secondary index moves with the value.
//
// Set is correct only when no indexed prop changed value: pure rename, or
// updating non-indexed props. The service layer should prefer UpdateAtomic
// for any user-facing update path; Set is retained for low-level paths
// where the caller has already reconciled indexes.
func (s *NodeStore) Set(ctx context.Context, n *node.Node, view *node.NodeView) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.set")(&err)
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		return nil, writeNodeRecords(tr, n, view)
	})
	return
}

// UpdateAtomic overwrites an existing node and reconciles its secondary
// property index entries against oldProps. For each name in indexedProps:
//
//  1. If the old value differed and was non-empty, the old index entry
//     is cleared.
//  2. If the new value is non-empty, a new index entry is written.
//
// All writes happen in a single FDB transaction so concurrent readers see
// either the full pre-state or the full post-state, never a half-rotated
// index.
func (s *NodeStore) UpdateAtomic(
	ctx context.Context,
	n *node.Node,
	view *node.NodeView,
	oldProps map[string]json.RawMessage,
	indexedProps []string,
	relationshipChanges ...node.RelationshipChanges,
) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.update_atomic")(&err)
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		if err := writeNodeRecords(tr, n, view); err != nil {
			return nil, err
		}
		reconcilePropertyIndexes(tr, n, oldProps, indexedProps)
		if err := applyRelationshipChanges(ctx, tr, relationshipChanges); err != nil {
			return nil, err
		}
		return nil, nil
	})
	return
}

func reconcilePropertyIndexes(
	tr fdb.Transaction,
	currentNode *node.Node,
	oldProps map[string]json.RawMessage,
	indexedProps []string,
) {
	for _, propName := range indexedProps {
		oldRaw := oldProps[propName]
		newRaw := currentNode.Props[propName]
		if bytes.Equal(oldRaw, newRaw) {
			continue
		}
		if len(oldRaw) > 0 {
			tr.Clear(fdb.Key(nodeByPropertyKey(
				currentNode.OrgID,
				currentNode.NodeType,
				propName,
				encodePropertyValue(oldRaw),
				currentNode.ID,
			)))
		}
		if len(newRaw) > 0 {
			tr.Set(fdb.Key(nodeByPropertyKey(
				currentNode.OrgID,
				currentNode.NodeType,
				propName,
				encodePropertyValue(newRaw),
				currentNode.ID,
			)), []byte{})
		}
	}
}

func applyRelationshipChanges(
	ctx context.Context,
	tr fdb.Transaction,
	relationshipChanges []node.RelationshipChanges,
) error {
	for _, changes := range relationshipChanges {
		for _, relationship := range changes.Remove {
			tr.Clear(fdb.Key(relationshipKey(
				relationship.OrgID,
				relationship.SourceID,
				relationship.RelationType,
				relationship.TargetID,
			)))
			tr.Clear(fdb.Key(relationshipReverseKey(
				relationship.OrgID,
				relationship.TargetID,
				relationship.RelationType,
				relationship.SourceID,
			)))
		}
		for _, relationship := range changes.Add {
			metadata, err := json.Marshal(relationship)
			if err != nil {
				slog.ErrorContext(ctx, "node.relationship.marshal_failed",
					slog.String("source_id", relationship.SourceID.String()),
					slog.String("relation_type", relationship.RelationType),
					slog.String("target_id", relationship.TargetID.String()),
					slog.String("err", err.Error()),
				)
				return fmt.Errorf("marshal relationship %s %s %s: %w", relationship.SourceID, relationship.RelationType, relationship.TargetID, err)
			}
			tr.Set(fdb.Key(relationshipKey(
				relationship.OrgID,
				relationship.SourceID,
				relationship.RelationType,
				relationship.TargetID,
			)), metadata)
			tr.Set(fdb.Key(relationshipReverseKey(
				relationship.OrgID,
				relationship.TargetID,
				relationship.RelationType,
				relationship.SourceID,
			)), []byte{})
		}
	}
	return nil
}

// writeNodeRecords emits the primary, resolve, and view records for n inside
// an existing transaction. Shared by Set and UpdateAtomic.
func writeNodeRecords(tr fdb.Transaction, n *node.Node, view *node.NodeView) error {
	nBytes, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("marshal node: %w", err)
	}
	tr.Set(fdb.Key(nodeInstanceKey(n.OrgID, n.NodeType, n.ID)), nBytes)

	resolveBytes, err := json.Marshal(&node.NodeResolve{
		OrgID:    n.OrgID,
		NodeType: n.NodeType,
	})
	if err != nil {
		return fmt.Errorf("marshal resolve: %w", err)
	}
	tr.Set(fdb.Key(nodeResolveKey(n.ID)), resolveBytes)

	if view != nil {
		viewBytes, err := json.Marshal(view)
		if err != nil {
			return fmt.Errorf("marshal view: %w", err)
		}
		tr.Set(fdb.Key(nodeViewKey(n.OrgID, n.NodeType, n.ID)), viewBytes)
	}
	return nil
}

// Delete removes the primary node, view, resolve, property indexes, and all
// relationships where the node is source or target. Uses range clears for the
// per-node relationship prefixes; no per-relationship scan required.
func (s *NodeStore) Delete(ctx context.Context, orgID, nodeID uuid.UUID) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.delete")(&err)
	// First discover NodeType via resolve so we can construct the instance and
	// view keys correctly.
	n, gerr := s.Get(ctx, orgID, nodeID)
	if gerr != nil {
		err = gerr
		return
	}
	if n == nil {
		return
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
			t, terr := tuple.Unpack(stripPrefix(kv.Key))
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
			t, terr := tuple.Unpack(stripPrefix(kv.Key))
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
	return
}

// CreateAtomic writes a new node plus initial relationships in one FDB
// transaction. indexedProps names the Props keys that should receive a
// property-index entry; the caller resolves this from the PropertyDef registry.
func (s *NodeStore) CreateAtomic(
	ctx context.Context,
	n *node.Node,
	view *node.NodeView,
	rels []*node.Relationship,
	indexedProps []string,
	idempotency *node.IdempotencyRecord,
) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.create_atomic")(&err)
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		if idempotency != nil {
			key := fdb.Key(idempotencyKey(n.OrgID, idempotency.Key))
			existing, err := tr.Get(key).Get()
			if err != nil {
				return nil, fmt.Errorf("read idempotency key: %w", err)
			}
			if len(existing) > 0 {
				return nil, fmt.Errorf("idempotency key %q already exists: %w", idempotency.Key, domain.ErrConflict)
			}
			recordBytes, err := json.Marshal(idempotency)
			if err != nil {
				return nil, fmt.Errorf("marshal idempotency record: %w", err)
			}
			tr.Set(key, recordBytes)
		}

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
	return
}

// EnsurePropertyIndex writes secondary-index entries for the given Props on
// an existing node. Idempotent: re-running for the same (node, prop, value)
// is a no-op. Used by one-off backfill commands when a new PropertyDef gains
// Indexed=true after nodes already exist.
func (s *NodeStore) EnsurePropertyIndex(ctx context.Context, n *node.Node, indexedProps []string) (err error) {
	defer telemetry.FDBOp(ctx, "store.node.ensure_property_index")(&err)
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		for _, propName := range indexedProps {
			raw, ok := n.Props[propName]
			if !ok || len(raw) == 0 {
				continue
			}
			tr.Set(fdb.Key(nodeByPropertyKey(n.OrgID, n.NodeType, propName, encodePropertyValue(raw), n.ID)), []byte{})
		}
		return nil, nil
	})
	return
}

// ListByProperty scans the secondary index for (orgID, nodeType, propName)
// narrowed to the given value, and returns the matching Node records.
func (s *NodeStore) ListByProperty(
	ctx context.Context,
	orgID uuid.UUID,
	nodeType, propName string,
	value json.RawMessage,
) (nodes []*node.Node, err error) {
	defer telemetry.FDBOp(ctx, "store.node.list_by_property")(&err)
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
			t, terr := tuple.Unpack(stripPrefix(kv.Key))
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
func (s *NodeStore) AllocateSequence(ctx context.Context, orgID, scopeNodeID uuid.UUID, nodeType string) (seq int64, err error) {
	defer telemetry.FDBOp(ctx, "store.node.allocate_sequence")(&err)
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

// LookupIdempotencyKey returns the record stamped under (orgID, key), or nil
// when the key has not been seen.
func (s *NodeStore) LookupIdempotencyKey(ctx context.Context, orgID uuid.UUID, key string) (record *node.IdempotencyRecord, err error) {
	defer telemetry.FDBOp(ctx, "store.node.lookup_idempotency")(&err)
	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(fdb.Key(idempotencyKey(orgID, key))).Get()
	})
	if err != nil {
		return nil, fmt.Errorf("fdb lookup idempotency: %w", err)
	}
	b, ok := val.([]byte)
	if !ok || len(b) == 0 {
		return nil, nil
	}
	return decodeIdempotencyRecord(key, b)
}
