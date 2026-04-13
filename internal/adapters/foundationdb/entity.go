package foundationdb

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// EntityStore implements node.EntityRepository using FoundationDB.
//
// Primary key:
//
//	node_instance, orgID, workspaceID, nodeType, nodeID → NodeValue (JSON)
//
// Secondary indexes:
//
//	node_instance_by_project, orgID, projectID, nodeType, nodeID    → workspaceID (16 bytes)
//	node_instance_by_state,   orgID, workspaceID, nodeType, stateID, nodeID → nil
//	node_by_property,         orgID, workspaceID, nodeType, propDefID, encodedValue, nodeID → nil
//
// Sequence counter:
//
//	sequence, orgID, "scope", scopeID, nodeType → int64 (little-endian)
type EntityStore struct {
	db fdb.Database
}

func NewEntityStore(db fdb.Database) *EntityStore {
	return &EntityStore{db: db}
}

func (s *EntityStore) Set(ctx context.Context, nv *node.NodeValue, props map[uuid.UUID]*node.PropertyValue, view *node.NodeListView) error {
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		primaryKey := nodeInstanceKey(nv.OrgID, nv.WorkspaceID, nv.NodeType, nv.ID)

		// Read the old NodeValue to clear stale state and project indexes.
		oldBytes := tr.Get(fdb.Key(primaryKey)).MustGet()
		if len(oldBytes) > 0 {
			var old node.NodeValue
			if err := json.Unmarshal(oldBytes, &old); err == nil {
				tr.Clear(fdb.Key(nodeByProjectKey(old.OrgID, old.ProjectID, old.NodeType, old.ID)))
				if old.StateID != nil {
					tr.Clear(fdb.Key(nodeByStateKey(old.OrgID, old.WorkspaceID, old.NodeType, *old.StateID, old.ID)))
				}
			}
		}

		// Clear old property values and their secondary index entries atomically.
		propPrefix := tuple.Tuple{keyPropertyValueOnNode, nv.OrgID.String(), nv.ID.String()}.Pack()
		propRange, err := fdb.PrefixRange(propPrefix)
		if err != nil {
			return nil, fmt.Errorf("property prefix range: %w", err)
		}
		oldProps := tr.GetRange(propRange, fdb.RangeOptions{}).GetSliceOrPanic()
		for _, kv := range oldProps {
			t, err := tuple.Unpack(kv.Key)
			if err != nil || len(t) < 4 {
				continue
			}
			defIDStr, ok := t[3].(string)
			if !ok {
				continue
			}
			defID, err := uuid.Parse(defIDStr)
			if err != nil {
				continue
			}
			var oldPV node.PropertyValue
			if json.Unmarshal(kv.Value, &oldPV) == nil {
				tr.Clear(fdb.Key(nodeByPropertyKey(nv.OrgID, nv.WorkspaceID, nv.NodeType, defID, encodeForIndex(&oldPV), nv.ID)))
			}
		}
		tr.ClearRange(propRange)

		// Write NodeValue.
		b, err := json.Marshal(nv)
		if err != nil {
			return nil, fmt.Errorf("marshal node value: %w", err)
		}
		tr.Set(fdb.Key(primaryKey), b)

		// Write project index (value = workspaceID bytes for hydration).
		wsBytes, _ := nv.WorkspaceID.MarshalBinary()
		tr.Set(fdb.Key(nodeByProjectKey(nv.OrgID, nv.ProjectID, nv.NodeType, nv.ID)), wsBytes)

		// Write state index.
		if nv.StateID != nil {
			tr.Set(fdb.Key(nodeByStateKey(nv.OrgID, nv.WorkspaceID, nv.NodeType, *nv.StateID, nv.ID)), []byte{})
		}

		// Write sequence index when entity has a sequence ID.
		if nv.SequenceID > 0 && nv.ProjectID != uuid.Nil {
			nodeIDBytes, _ := nv.ID.MarshalBinary()
			tr.Set(fdb.Key(nodeBySequenceKey(nv.OrgID, nv.ProjectID, nv.NodeType, int64(nv.SequenceID))), nodeIDBytes)
		}

		// Write new property values and secondary index entries.
		for defID, pv := range props {
			pvBytes, err := json.Marshal(pv)
			if err != nil {
				return nil, fmt.Errorf("marshal property value: %w", err)
			}
			pvKey := tuple.Tuple{keyPropertyValueOnNode, nv.OrgID.String(), nv.ID.String(), defID.String()}.Pack()
			tr.Set(fdb.Key(pvKey), pvBytes)
			tr.Set(fdb.Key(nodeByPropertyKey(nv.OrgID, nv.WorkspaceID, nv.NodeType, defID, encodeForIndex(pv), nv.ID)), []byte{})
		}

		// Always write the global resolution record so any service can look up
		// org/workspace/type by nodeID without prior org context.
		resolveBytes, err := json.Marshal(&node.NodeResolve{
			OrgID:       nv.OrgID,
			WorkspaceID: nv.WorkspaceID,
			ProjectID:   nv.ProjectID,
			NodeType:    nv.NodeType,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal node resolve: %w", err)
		}
		tr.Set(fdb.Key(nodeResolveKey(nv.ID)), resolveBytes)

		// Write the materialized list view when provided.
		if view != nil {
			viewBytes, err := json.Marshal(view)
			if err != nil {
				return nil, fmt.Errorf("marshal node list view: %w", err)
			}
			tr.Set(fdb.Key(nodeListViewKey(nv.OrgID, nv.WorkspaceID, nv.NodeType, nv.ID)), viewBytes)
		}

		return nil, nil
	})
	if err != nil {
		telemetry.L(ctx).Error("fdb.entity.Set failed",
			slog.String("node_id", nv.ID.String()),
			slog.String("node_type", nv.NodeType),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("entity set: %w", err)
	}
	return nil
}

func (s *EntityStore) Get(_ context.Context, orgID, workspaceID uuid.UUID, nodeType string, nodeID uuid.UUID) (*node.NodeValue, error) {
	key := nodeInstanceKey(orgID, workspaceID, nodeType, nodeID)
	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(fdb.Key(key)).Get()
	})
	if err != nil {
		return nil, fmt.Errorf("entity get: %w", err)
	}
	b, ok := val.([]byte)
	if !ok || len(b) == 0 {
		return nil, nil
	}
	var nv node.NodeValue
	if err := json.Unmarshal(b, &nv); err != nil {
		return nil, fmt.Errorf("unmarshal node value: %w", err)
	}
	return &nv, nil
}

func (s *EntityStore) Delete(ctx context.Context, nv *node.NodeValue) error {
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Clear(fdb.Key(nodeInstanceKey(nv.OrgID, nv.WorkspaceID, nv.NodeType, nv.ID)))
		tr.Clear(fdb.Key(nodeByProjectKey(nv.OrgID, nv.ProjectID, nv.NodeType, nv.ID)))
		if nv.StateID != nil {
			tr.Clear(fdb.Key(nodeByStateKey(nv.OrgID, nv.WorkspaceID, nv.NodeType, *nv.StateID, nv.ID)))
		}

		// Clear property values and their secondary index entries.
		propPrefix := tuple.Tuple{keyPropertyValueOnNode, nv.OrgID.String(), nv.ID.String()}.Pack()
		propRange, err := fdb.PrefixRange(propPrefix)
		if err != nil {
			return nil, err
		}
		oldProps := tr.GetRange(propRange, fdb.RangeOptions{}).GetSliceOrPanic()
		for _, kv := range oldProps {
			t, err := tuple.Unpack(kv.Key)
			if err != nil || len(t) < 4 {
				continue
			}
			defIDStr, ok := t[3].(string)
			if !ok {
				continue
			}
			defID, err := uuid.Parse(defIDStr)
			if err != nil {
				continue
			}
			var pv node.PropertyValue
			if json.Unmarshal(kv.Value, &pv) == nil {
				tr.Clear(fdb.Key(nodeByPropertyKey(nv.OrgID, nv.WorkspaceID, nv.NodeType, defID, encodeForIndex(&pv), nv.ID)))
			}
		}
		tr.ClearRange(propRange)

		// Clear resolution record and materialized view.
		tr.Clear(fdb.Key(nodeResolveKey(nv.ID)))
		tr.Clear(fdb.Key(nodeListViewKey(nv.OrgID, nv.WorkspaceID, nv.NodeType, nv.ID)))

		return nil, nil
	})
	if err != nil {
		telemetry.L(ctx).Error("fdb.entity.Delete failed",
			slog.String("node_id", nv.ID.String()),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("entity delete: %w", err)
	}
	return nil
}

func (s *EntityStore) ListByProject(_ context.Context, orgID, projectID uuid.UUID, nodeType string) ([]*node.NodeValue, error) {
	prefix := tuple.Tuple{keyNodeInstanceByProject, orgID.String(), projectID.String(), nodeType}.Pack()
	pr, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, err
	}
	idxVals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("entity list by project index: %w", err)
	}
	kvs := idxVals.([]fdb.KeyValue)
	if len(kvs) == 0 {
		return nil, nil
	}

	type entry struct {
		workspaceID uuid.UUID
		nodeID      uuid.UUID
	}
	entries := make([]entry, 0, len(kvs))
	for _, kv := range kvs {
		// value = workspaceID (16 bytes binary)
		if len(kv.Value) < 16 {
			continue
		}
		wsID, err := uuid.FromBytes(kv.Value[:16])
		if err != nil {
			continue
		}
		t, err := tuple.Unpack(kv.Key)
		if err != nil || len(t) < 5 {
			continue
		}
		nodeIDStr, ok := t[4].(string)
		if !ok {
			continue
		}
		nodeID, err := uuid.Parse(nodeIDStr)
		if err != nil {
			continue
		}
		entries = append(entries, entry{wsID, nodeID})
	}
	if len(entries) == 0 {
		return nil, nil
	}

	// Batch-read primary values in one transaction using parallel futures.
	primaries, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		futures := make([]fdb.FutureByteSlice, len(entries))
		for i, e := range entries {
			futures[i] = tr.Get(fdb.Key(nodeInstanceKey(orgID, e.workspaceID, nodeType, e.nodeID)))
		}
		results := make([][]byte, len(futures))
		for i, f := range futures {
			b, err := f.Get()
			if err != nil {
				return nil, err
			}
			results[i] = b
		}
		return results, nil
	})
	if err != nil {
		return nil, fmt.Errorf("entity list by project hydrate: %w", err)
	}
	return unmarshalNodeValues(primaries.([][]byte)), nil
}

func (s *EntityStore) ListByState(_ context.Context, orgID, workspaceID uuid.UUID, nodeType string, stateID uuid.UUID) ([]*node.NodeValue, error) {
	prefix := tuple.Tuple{keyNodeInstanceByState, orgID.String(), workspaceID.String(), nodeType, stateID.String()}.Pack()
	pr, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, err
	}
	idxVals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("entity list by state index: %w", err)
	}
	kvs := idxVals.([]fdb.KeyValue)
	// key: (keyNodeInstanceByState, orgID, workspaceID, nodeType, stateID, nodeID)
	nodeIDs := extractLastUUIDs(kvs, 5)
	return s.batchGet(orgID, workspaceID, nodeType, nodeIDs)
}

func (s *EntityStore) ListByProperty(_ context.Context, orgID, workspaceID uuid.UUID, nodeType string, propDefID uuid.UUID, pv *node.PropertyValue) ([]*node.NodeValue, error) {
	encoded := encodeForIndex(pv)
	prefix := tuple.Tuple{keyNodeByProperty, orgID.String(), workspaceID.String(), nodeType, propDefID.String(), encoded}.Pack()
	pr, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, err
	}
	idxVals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("entity list by property index: %w", err)
	}
	kvs := idxVals.([]fdb.KeyValue)
	// key: (keyNodeByProperty, orgID, workspaceID, nodeType, propDefID, encodedValue, nodeID)
	nodeIDs := extractLastUUIDs(kvs, 6)
	return s.batchGet(orgID, workspaceID, nodeType, nodeIDs)
}

func (s *EntityStore) AllocateSequenceID(_ context.Context, orgID, projectID uuid.UUID, nodeType string) (int64, error) {
	key := fdb.Key(tuple.Tuple{keySequence, orgID.String(), "scope", projectID.String(), nodeType}.Pack())
	val, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		b, err := tr.Get(key).Get()
		if err != nil {
			return nil, err
		}
		var current int64
		if len(b) >= 8 {
			current = int64(binary.LittleEndian.Uint64(b))
		}
		next := current + 1
		buf := make([]byte, 8)
		binary.LittleEndian.PutUint64(buf, uint64(next))
		tr.Set(key, buf)
		return next, nil
	})
	if err != nil {
		return 0, fmt.Errorf("allocate sequence id: %w", err)
	}
	return val.(int64), nil
}

// GetBySequence returns the nodeID for the given sequence number within a project.
// Returns uuid.Nil, nil if no entity with that sequence number exists.
func (s *EntityStore) GetBySequence(ctx context.Context, orgID, projectID uuid.UUID, nodeType string, seqID int64) (uuid.UUID, error) {
	key := fdb.Key(nodeBySequenceKey(orgID, projectID, nodeType, seqID))
	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(key).Get()
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("get by sequence: %w", err)
	}
	b, ok := val.([]byte)
	if !ok || len(b) == 0 {
		return uuid.Nil, nil
	}
	var id uuid.UUID
	if err := id.UnmarshalBinary(b); err != nil {
		return uuid.Nil, fmt.Errorf("unmarshal node id from sequence index: %w", err)
	}
	return id, nil
}

func (s *EntityStore) CreateAtomic(ctx context.Context, orgID, projectID uuid.UUID, nv *node.NodeValue, props map[uuid.UUID]*node.PropertyValue, view *node.NodeListView, assigneeIDs []uuid.UUID, labelIDs []uuid.UUID, actorID uuid.UUID) (int64, error) {
	now := time.Now().UTC()
	var seqID int64

	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		// 1. Allocate sequence ID inline when the entity type uses sequences.
		if projectID != uuid.Nil {
			seqKey := fdb.Key(tuple.Tuple{keySequence, orgID.String(), "scope", projectID.String(), nv.NodeType}.Pack())
			b, err := tr.Get(seqKey).Get()
			if err != nil {
				return nil, fmt.Errorf("read sequence: %w", err)
			}
			var current int64
			if len(b) >= 8 {
				current = int64(binary.LittleEndian.Uint64(b))
			}
			seqID = current + 1
			buf := make([]byte, 8)
			binary.LittleEndian.PutUint64(buf, uint64(seqID))
			tr.Set(seqKey, buf)
			nv.SequenceID = int32(seqID)
			if view != nil {
				view.SequenceID = int32(seqID)
			}
			// Write sequence → nodeID index for O(1) lookup by identifier.
			nodeIDBytes, _ := nv.ID.MarshalBinary()
			tr.Set(fdb.Key(nodeBySequenceKey(orgID, projectID, nv.NodeType, seqID)), nodeIDBytes)
		}

		// 2. Write NodeValue primary record.
		nvBytes, err := json.Marshal(nv)
		if err != nil {
			return nil, fmt.Errorf("marshal node value: %w", err)
		}
		tr.Set(fdb.Key(nodeInstanceKey(nv.OrgID, nv.WorkspaceID, nv.NodeType, nv.ID)), nvBytes)

		// 3. Write secondary indexes.
		wsBytes, _ := nv.WorkspaceID.MarshalBinary()
		tr.Set(fdb.Key(nodeByProjectKey(nv.OrgID, nv.ProjectID, nv.NodeType, nv.ID)), wsBytes)
		if nv.StateID != nil {
			tr.Set(fdb.Key(nodeByStateKey(nv.OrgID, nv.WorkspaceID, nv.NodeType, *nv.StateID, nv.ID)), []byte{})
		}

		// 4. Write property values and property secondary indexes.
		for defID, pv := range props {
			pvBytes, err := json.Marshal(pv)
			if err != nil {
				return nil, fmt.Errorf("marshal property value: %w", err)
			}
			pvKey := tuple.Tuple{keyPropertyValueOnNode, nv.OrgID.String(), nv.ID.String(), defID.String()}.Pack()
			tr.Set(fdb.Key(pvKey), pvBytes)
			tr.Set(fdb.Key(nodeByPropertyKey(nv.OrgID, nv.WorkspaceID, nv.NodeType, defID, encodeForIndex(pv), nv.ID)), []byte{})
		}

		// 5. Write global resolution record.
		resolveBytes, err := json.Marshal(&node.NodeResolve{
			OrgID:       nv.OrgID,
			WorkspaceID: nv.WorkspaceID,
			ProjectID:   nv.ProjectID,
			NodeType:    nv.NodeType,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal node resolve: %w", err)
		}
		tr.Set(fdb.Key(nodeResolveKey(nv.ID)), resolveBytes)

		// 6. Write NodeListView if provided.
		if view != nil {
			viewBytes, err := json.Marshal(view)
			if err != nil {
				return nil, fmt.Errorf("marshal node list view: %w", err)
			}
			tr.Set(fdb.Key(nodeListViewKey(nv.OrgID, nv.WorkspaceID, nv.NodeType, nv.ID)), viewBytes)
		}

		// 7. Write initial assignments (no pre-clearing needed — entity is new).
		if len(assigneeIDs) > 0 {
			aVal, _ := json.Marshal(AssignmentValue{AssignedBy: actorID, AssignedAt: now})
			orgStr := nv.OrgID.String()
			nodeStr := nv.ID.String()
			nanoTS := now.UnixNano()
			for _, uid := range assigneeIDs {
				uidStr := uid.String()
				tr.Set(fdb.Key(tuple.Tuple{keyAssignmentOnNode, orgStr, nodeStr, uidStr}.Pack()), aVal)
				tr.Set(fdb.Key(tuple.Tuple{keyAssignmentToUser, orgStr, uidStr, nanoTS, nodeStr}.Pack()), []byte{})
			}
		}

		// 8. Write initial labels (no pre-clearing needed — entity is new).
		if len(labelIDs) > 0 {
			lVal, _ := json.Marshal(LabelOnNodeValue{AddedBy: actorID, AddedAt: now})
			orgStr := nv.OrgID.String()
			nodeStr := nv.ID.String()
			for _, lid := range labelIDs {
				lidStr := lid.String()
				tr.Set(fdb.Key(tuple.Tuple{keyLabelOnNode, orgStr, nodeStr, lidStr}.Pack()), lVal)
				tr.Set(fdb.Key(tuple.Tuple{keyNodesWithLabel, orgStr, lidStr, nodeStr}.Pack()), []byte{})
			}
		}

		return nil, nil
	})
	if err != nil {
		telemetry.L(ctx).Error("fdb.entity.CreateAtomic failed",
			slog.String("node_id", nv.ID.String()),
			slog.String("node_type", nv.NodeType),
			slog.String("project_id", projectID.String()),
			slog.String("err", err.Error()),
		)
		return 0, fmt.Errorf("entity create atomic: %w", err)
	}
	return seqID, nil
}

// batchGet fetches multiple NodeValues by nodeID in a single read transaction
// using parallel futures.
func (s *EntityStore) batchGet(orgID, workspaceID uuid.UUID, nodeType string, nodeIDs []uuid.UUID) ([]*node.NodeValue, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	primaries, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		futures := make([]fdb.FutureByteSlice, len(nodeIDs))
		for i, id := range nodeIDs {
			futures[i] = tr.Get(fdb.Key(nodeInstanceKey(orgID, workspaceID, nodeType, id)))
		}
		results := make([][]byte, len(futures))
		for i, f := range futures {
			b, err := f.Get()
			if err != nil {
				return nil, err
			}
			results[i] = b
		}
		return results, nil
	})
	if err != nil {
		return nil, fmt.Errorf("batch get node values: %w", err)
	}
	return unmarshalNodeValues(primaries.([][]byte)), nil
}

// unmarshalNodeValues deserializes a slice of raw JSON bytes into NodeValues,
// skipping any empty or unparseable entries.
func unmarshalNodeValues(raw [][]byte) []*node.NodeValue {
	result := make([]*node.NodeValue, 0, len(raw))
	for _, b := range raw {
		if len(b) == 0 {
			continue
		}
		var nv node.NodeValue
		if err := json.Unmarshal(b, &nv); err != nil {
			continue
		}
		result = append(result, &nv)
	}
	return result
}

// encodeForIndex converts a PropertyValue to a tuple element suitable for use
// as the sort key in a node_by_property secondary index.
// Enum values use EnumRank (int64) so they sort by rank, not key string.
func encodeForIndex(pv *node.PropertyValue) interface{} {
	if pv == nil {
		return ""
	}
	switch pv.Kind {
	case node.PropertyValueText:
		if pv.Text != nil {
			return *pv.Text
		}
		return ""
	case node.PropertyValueEnum:
		if pv.EnumRank != nil {
			return int64(*pv.EnumRank)
		}
		if pv.Enum != nil {
			return *pv.Enum
		}
		return ""
	case node.PropertyValueInt:
		if pv.Int != nil {
			return *pv.Int
		}
		return int64(0)
	case node.PropertyValueFloat:
		if pv.Float != nil {
			return *pv.Float
		}
		return float64(0)
	case node.PropertyValueBool:
		if pv.Bool != nil && *pv.Bool {
			return int64(1)
		}
		return int64(0)
	case node.PropertyValueTimestamp:
		if pv.Timestamp != nil {
			return pv.Timestamp.UnixNano()
		}
		return int64(0)
	default:
		return ""
	}
}

// Key constructors.

func nodeInstanceKey(orgID, workspaceID uuid.UUID, nodeType string, nodeID uuid.UUID) []byte {
	return tuple.Tuple{keyNodeInstance, orgID.String(), workspaceID.String(), nodeType, nodeID.String()}.Pack()
}

func nodeByProjectKey(orgID, projectID uuid.UUID, nodeType string, nodeID uuid.UUID) []byte {
	return tuple.Tuple{keyNodeInstanceByProject, orgID.String(), projectID.String(), nodeType, nodeID.String()}.Pack()
}

func nodeByStateKey(orgID, workspaceID uuid.UUID, nodeType string, stateID, nodeID uuid.UUID) []byte {
	return tuple.Tuple{keyNodeInstanceByState, orgID.String(), workspaceID.String(), nodeType, stateID.String(), nodeID.String()}.Pack()
}

func nodeByPropertyKey(orgID, workspaceID uuid.UUID, nodeType string, propDefID uuid.UUID, encoded interface{}, nodeID uuid.UUID) []byte {
	return tuple.Tuple{keyNodeByProperty, orgID.String(), workspaceID.String(), nodeType, propDefID.String(), encoded, nodeID.String()}.Pack()
}

func nodeListViewKey(orgID, workspaceID uuid.UUID, nodeType string, nodeID uuid.UUID) []byte {
	return tuple.Tuple{keyNodeListView, orgID.String(), workspaceID.String(), nodeType, nodeID.String()}.Pack()
}

// nodeResolveKey is intentionally NOT org-scoped: the whole point is to allow
// lookup by nodeID alone without knowing org context upfront.
func nodeResolveKey(nodeID uuid.UUID) []byte {
	return tuple.Tuple{keyNodeResolve, nodeID.String()}.Pack()
}

func nodeBySequenceKey(orgID, projectID uuid.UUID, nodeType string, seqID int64) []byte {
	return tuple.Tuple{keyNodeBySequence, orgID.String(), projectID.String(), nodeType, seqID}.Pack()
}

// slugIndexKey returns a global slug index key: (slug_index, nodeType, slug) -> nodeID bytes.
const keySlugIndex = "slug_index"

func slugIndexKey(nodeType, slug string) []byte {
	return tuple.Tuple{keySlugIndex, nodeType, slug}.Pack()
}

// GetBySlug resolves a global slug index entry to a node UUID.
// Returns domain.ErrNotFound when no entry exists.
func (s *EntityStore) GetBySlug(ctx context.Context, nodeType, slug string) (uuid.UUID, error) {
	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(fdb.Key(slugIndexKey(nodeType, slug))).Get()
	})
	if err != nil {
		telemetry.L(ctx).Warn("fdb.entity.GetBySlug failed", slog.String("node_type", nodeType), slog.String("slug", slug), slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("slug lookup %s/%s: %w", nodeType, slug, err)
	}
	b, ok := val.([]byte)
	if !ok || len(b) == 0 {
		return uuid.Nil, domain.ErrNotFound
	}
	id, err := uuid.FromBytes(b)
	if err != nil {
		return uuid.Nil, fmt.Errorf("unmarshal slug index %s/%s: %w", nodeType, slug, err)
	}
	return id, nil
}

// WriteSlugIndex writes a global slug index entry.
func (s *EntityStore) WriteSlugIndex(_ context.Context, nodeType, slug string, nodeID uuid.UUID) error {
	b, _ := nodeID.MarshalBinary()
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(fdb.Key(slugIndexKey(nodeType, slug)), b)
		return nil, nil
	})
	return err
}

// DeleteSlugIndex removes a global slug index entry.
func (s *EntityStore) DeleteSlugIndex(_ context.Context, nodeType, slug string) error {
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Clear(fdb.Key(slugIndexKey(nodeType, slug)))
		return nil, nil
	})
	return err
}
