package foundationdb

import (
	"context"
	"encoding/json"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// mutationSource represents the primary record of one node that a transaction
// read. Exists is false if the resolution record in the organization or the
// primary record does not exist.
type mutationSource struct {
	Exists   bool
	TypeKey  string
	Encoding []byte
	Node     node.Node
}

// ReadMutationSnapshot reads the node, its relationships, and the schema for
// orgID in one transaction.
func (s *NodeStore) ReadMutationSnapshot(ctx context.Context, orgID, nodeID uuid.UUID) (snapshot node.MutationSnapshot, schema node.SchemaSnapshot, err error) {
	defer telemetry.FDBOp(ctx, "store.node.read_mutation_snapshot")(&err)
	err = runNodeReadTransaction(ctx, s.db, "read mutation snapshot of node "+nodeID.String(), func(tr fdb.Transaction) error {
		var readErr error
		snapshot, readErr = readNodeSnapshot(ctx, tr, orgID, nodeID)
		if readErr != nil {
			return readErr
		}
		schema, readErr = readSchemaSnapshot(ctx, tr, orgID)
		return readErr
	})
	return snapshot, schema, err
}

// readNodeSnapshot reads the primary record and relationships of nodeID
// inside tr.
func readNodeSnapshot(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) (node.MutationSnapshot, error) {
	snapshot := node.MutationSnapshot{
		Exists: false, Node: absentNode(), Encoding: nil, PrimaryDigest: node.Digest{}, ParentID: uuid.Nil,
		Outgoing: nil, Incoming: nil, IncomingOverflow: false,
	}
	source, err := readMutationSource(ctx, tr, orgID, nodeID)
	if err != nil || !source.Exists {
		return snapshot, err
	}
	outgoing, err := readOutgoingSnapshots(ctx, tr, orgID, nodeID, "")
	if err != nil {
		return snapshot, err
	}
	incoming, overflow, err := readIncomingSnapshots(ctx, tr, orgID, nodeID)
	if err != nil {
		return snapshot, err
	}
	snapshot.Exists, snapshot.Node, snapshot.Encoding = true, source.Node, source.Encoding
	snapshot.PrimaryDigest = node.PrimaryDigest(source.Encoding)
	snapshot.ParentID = storedUUID(source.Node.Props[parentIDProperty])
	snapshot.Outgoing, snapshot.Incoming, snapshot.IncomingOverflow = outgoing, incoming, overflow
	return snapshot, nil
}

// storedUUID decodes raw as a JSON string containing a UUID. storedUUID returns
// [uuid.Nil] when raw is absent, is not a JSON string, or does not contain a
// UUID.
func storedUUID(raw json.RawMessage) uuid.UUID {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return uuid.Nil
	}
	parsed, err := uuid.Parse(text)
	if err != nil {
		return uuid.Nil
	}
	return parsed
}

// absentNode returns a zero node when the snapshot lacks a primary record.
func absentNode() node.Node {
	return node.Node{
		ID: uuid.Nil, OrgID: uuid.Nil, NodeType: "", Name: "", Props: nil,
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: time.Time{}, UpdatedAt: time.Time{},
	}
}

// readMutationSource reads the resolution record and primary record of nodeID
// inside tr.
func readMutationSource(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) (mutationSource, error) {
	absent := mutationSource{Exists: false, TypeKey: "", Encoding: nil, Node: absentNode()}
	resolved, err := tr.Get(fdb.Key(nodeResolveKey(nodeID))).Get()
	if err != nil {
		return absent, searchReadFailure(ctx, "read node resolution "+nodeID.String(), err)
	}
	if len(resolved) == 0 {
		return absent, nil
	}
	var resolve node.NodeResolve
	if err := json.Unmarshal(resolved, &resolve); err != nil {
		return absent, nodeOperationFailure(ctx, "decode node resolution "+nodeID.String(), err)
	}
	if resolve.OrgID != orgID {
		return absent, nil
	}
	encoded, err := tr.Get(fdb.Key(nodeInstanceKey(orgID, resolve.NodeType, nodeID))).Get()
	if err != nil {
		return absent, searchReadFailure(ctx, "read node "+nodeID.String(), err)
	}
	if len(encoded) == 0 {
		return absent, nil
	}
	var current node.Node
	if err := json.Unmarshal(encoded, &current); err != nil {
		return absent, nodeOperationFailure(ctx, "decode node "+nodeID.String(), err)
	}
	return mutationSource{Exists: true, TypeKey: resolve.NodeType, Encoding: encoded, Node: current}, nil
}

// readOutgoingSnapshots reads every relationship of relationType from nodeID
// inside tr. It reads every type when relationType is empty.
func readOutgoingSnapshots(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID, relationType string) ([]node.RelationshipSnapshot, error) {
	keyRange, err := fdb.PrefixRange(relationshipPrefixBySource(orgID, nodeID, relationType))
	if err != nil {
		return nil, nodeOperationFailure(ctx, "create relationship range for node "+nodeID.String(), err)
	}
	items, err := tr.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read relationships of node "+nodeID.String(), err)
	}
	snapshots := make([]node.RelationshipSnapshot, 0, len(items))
	for _, item := range items {
		snapshot, err := decodeRelationshipSnapshot(ctx, item.Value)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

// readIncomingSnapshots reads relationships to nodeID inside tr. If their
// count exceeds node.MutationEdgeLimit, it reports true and does not return
// relationships. readDeleteEdges reads more than that limit.
func readIncomingSnapshots(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) ([]node.RelationshipSnapshot, bool, error) {
	edges, err := readDeleteEdges(ctx, tr, orgID, nodeID, "", true)
	if err != nil {
		return nil, false, err
	}
	if len(edges) > node.MutationEdgeLimit {
		return nil, true, nil
	}
	futures := make([]fdb.FutureByteSlice, 0, len(edges))
	for _, edge := range edges {
		futures = append(futures, tr.Get(fdb.Key(relationshipKey(orgID, edge.SourceID, edge.RelationType, edge.TargetID))))
	}
	snapshots := make([]node.RelationshipSnapshot, 0, len(edges))
	for position, future := range futures {
		metadata, err := future.Get()
		if err != nil {
			return nil, false, searchReadFailure(ctx, "read relationship from "+edges[position].SourceID.String(), err)
		}
		snapshot, err := decodeRelationshipSnapshot(ctx, metadata)
		if err != nil {
			return nil, false, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, false, nil
}

// decodeRelationshipSnapshot decodes the stored metadata of one relationship
// and returns the relationship with its digest.
func decodeRelationshipSnapshot(ctx context.Context, metadata []byte) (node.RelationshipSnapshot, error) {
	var relationship node.Relationship
	if err := json.Unmarshal(metadata, &relationship); err != nil {
		return node.RelationshipSnapshot{}, nodeOperationFailure(ctx, "decode relationship metadata", err)
	}
	return node.RelationshipSnapshot{Relationship: relationship, Digest: node.RelationshipDigest(metadata)}, nil
}
