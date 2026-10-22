package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// nodeTarget returns the audit target of one node record.
func nodeTarget(current *node.Node) audit.Target {
	return nodeIdentityTarget(current.ID, current.NodeType, current.Name)
}

// nodeIdentityTarget returns the audit target of the node with nodeID, its
// node type, and its name.
func nodeIdentityTarget(nodeID uuid.UUID, nodeType, name string) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindNode, NodeType: nodeType, Reference: "", Name: name, URI: "",
		RelationType: "", ID: nodeID, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// relationshipTarget returns the audit target of one relationship.
func relationshipTarget(sourceID uuid.UUID, relationType string, targetID uuid.UUID) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindRelationship, NodeType: "", Reference: "", Name: "", URI: "",
		RelationType: relationType, ID: uuid.Nil, SourceID: sourceID, TargetID: targetID,
	}
}

// applyRelationshipChanges applies each set in changesList in order inside
// tr. Each set clears its removed relationships before it writes its added
// relationships. Before each clear or write, it reads the stored value inside
// tr and records the actual change in changes.
func applyRelationshipChanges(ctx context.Context, tr fdb.Transaction, changesList []node.RelationshipChanges, changes *mutationAudit) error {
	for _, set := range changesList {
		if err := removeRelationships(ctx, tr, set.Remove, changes); err != nil {
			return err
		}
		if err := addRelationships(ctx, tr, set.Add, changes); err != nil {
			return err
		}
	}
	return nil
}

// removeRelationships clears each relationship inside tr and records its
// stored value as removed. A relationship that does not exist records
// nothing.
func removeRelationships(ctx context.Context, tr fdb.Transaction, removed []*node.Relationship, changes *mutationAudit) error {
	stored, err := readRelationshipValues(ctx, tr, removed)
	if err != nil {
		return err
	}
	for position, relationship := range removed {
		tr.Clear(fdb.Key(relationshipKey(relationship.OrgID, relationship.SourceID, relationship.RelationType, relationship.TargetID)))
		tr.Clear(fdb.Key(relationshipReverseKey(relationship.OrgID, relationship.TargetID, relationship.RelationType, relationship.SourceID)))
		target := relationshipTarget(relationship.SourceID, relationship.RelationType, relationship.TargetID)
		if err := changes.record(target, stored[position], nil); err != nil {
			return nodeOperationFailure(ctx, "record a removed relationship", err)
		}
	}
	return nil
}

// addRelationships writes each relationship inside tr and records the
// stored value before the write and the written value.
func addRelationships(ctx context.Context, tr fdb.Transaction, added []*node.Relationship, changes *mutationAudit) error {
	stored, err := readRelationshipValues(ctx, tr, added)
	if err != nil {
		return err
	}
	for position, relationship := range added {
		metadata, err := json.Marshal(relationship)
		if err != nil {
			wrapped := fmt.Errorf("marshal relationship %s %s %s: %w", relationship.SourceID, relationship.RelationType, relationship.TargetID, err)
			telemetry.L(ctx).ErrorContext(ctx, "node.relationship.marshal_failed", slog.String("err", wrapped.Error()), slog.String("source_id", relationship.SourceID.String()), slog.String("target_id", relationship.TargetID.String()))
			return loggedSearchError{err: wrapped}
		}
		tr.Set(fdb.Key(relationshipKey(relationship.OrgID, relationship.SourceID, relationship.RelationType, relationship.TargetID)), metadata)
		tr.Set(fdb.Key(relationshipReverseKey(relationship.OrgID, relationship.TargetID, relationship.RelationType, relationship.SourceID)), []byte{})
		target := relationshipTarget(relationship.SourceID, relationship.RelationType, relationship.TargetID)
		if err := changes.record(target, stored[position], metadata); err != nil {
			return nodeOperationFailure(ctx, "record an added relationship", err)
		}
	}
	return nil
}

// readRelationshipValues reads the stored forward value of each relationship
// inside tr. It issues every read before it waits on any of them. A missing
// relationship returns a nil value.
func readRelationshipValues(ctx context.Context, tr fdb.Transaction, relationships []*node.Relationship) ([][]byte, error) {
	futures := make([]fdb.FutureByteSlice, 0, len(relationships))
	for _, relationship := range relationships {
		futures = append(futures, tr.Get(fdb.Key(relationshipKey(relationship.OrgID, relationship.SourceID, relationship.RelationType, relationship.TargetID))))
	}
	values := make([][]byte, 0, len(relationships))
	for position, future := range futures {
		value, err := future.Get()
		if err != nil {
			relationship := relationships[position]
			return nil, searchReadFailure(ctx, "read relationship "+relationship.SourceID.String()+" "+relationship.RelationType+" "+relationship.TargetID.String(), err)
		}
		values = append(values, value)
	}
	return values, nil
}
