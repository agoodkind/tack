package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// UpdateInput holds optional fields to update.
type UpdateInput struct {
	NodeID              uuid.UUID
	Name                *string
	Props               map[string]json.RawMessage // merged on top of existing
	RelationshipChanges node.RelationshipChanges
	ActorID             uuid.UUID
	// ScopeID, when not [uuid.Nil], replaces the stored scope_id. It requires a
	// parent change in the same update. Internal callers that move a node into
	// another scope set it. Props cannot set scope_id.
	ScopeID uuid.UUID `exhaustruct:"optional"`
}

// Update rewrites a node's name and/or Props. Property index entries for
// indexed props are refreshed when the value changes.
func (s *NodeService) Update(ctx context.Context, in UpdateInput) (*node.NodeView, error) {
	ctx, span := telemetry.StartSpan(ctx, "service.node.update",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("node.id", in.NodeID.String())),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("node_id", in.NodeID.String()))
	log := telemetry.L(ctx)

	existing, err := s.reader.Get(ctx, in.NodeID)
	if err != nil {
		log.ErrorContext(ctx, "node.update_read_failed", slog.String("node_id", in.NodeID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("read node %s for its update: %w", in.NodeID, err)
	}
	if existing == nil {
		return nil, domain.ErrNotFound
	}
	snapshot, schema, err := s.readMutationSnapshot(ctx, existing.OrgID, in.NodeID)
	if err != nil {
		return nil, err
	}
	if !snapshot.Exists {
		return nil, domain.ErrNotFound
	}
	source := snapshot.Node
	nt := schema.Types[source.NodeType]
	if nt == nil {
		return nil, fmt.Errorf("node type %q: %w", source.NodeType, domain.ErrNotFound)
	}
	if err := s.validateUpdateProps(ctx, source.OrgID, nt, in.Props); err != nil {
		return nil, err
	}
	parent, otherChanges, err := s.parentChangeForUpdate(ctx, snapshot, in.Props, in.RelationshipChanges)
	if err != nil {
		return nil, err
	}
	props, err := scopedUpdateProps(ctx, in, parent.Change)
	if err != nil {
		return nil, err
	}
	merged := mergedUpdateProps(source.Props, props, parent.Change)
	// An update can change a value that a reference renders from, such as the
	// scope pointer or numbered property. Update renders reference keys from
	// the merged properties and claims them. The store refuses a move to a
	// claimed reference and does not leave a stale claim. A type without a
	// template does not render keys, and the store keeps the stored claims.
	referenceKeys, err := s.referenceKeysFor(ctx, source.OrgID, nt, updateScopeID(merged), merged)
	if err != nil {
		return nil, err
	}
	referenceRecords, err := s.referenceGraph(ctx, source.OrgID, schema.Schema, setUpdateProps(props))
	if err != nil {
		return nil, err
	}
	plan := node.MutationPlan{
		OrgID: source.OrgID, NodeID: source.ID, NodeType: source.NodeType, ActorID: in.ActorID,
		Source:        node.SourceExpectation{Absent: false, PrimaryDigest: snapshot.PrimaryDigest},
		MetadataEpoch: schema.MetadataEpoch,
		Graph: node.GraphExpectation{
			Records: sortedGraphRecords(parent.Records, referenceRecords), Relationships: parent.Relationships, Incoming: nil,
		},
		Name: in.Name, Props: propsWithoutParent(props), Parent: parent.Change, ReferenceKeys: referenceKeys,
		Relationships: stampRelationshipChanges(otherChanges, in.ActorID, clock.Now().UTC()), Idempotency: nil,
	}
	if _, err := s.nodes.ApplyMutation(ctx, plan); err != nil {
		log.ErrorContext(ctx, "node.update_failed", slog.String("node_id", in.NodeID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("update node: %w", err)
	}
	return s.committedView(ctx, in.NodeID)
}

// committedView reads the view of nodeID after a mutation commits.
func (s *NodeService) committedView(ctx context.Context, nodeID uuid.UUID) (*node.NodeView, error) {
	view, err := s.reader.Get(ctx, nodeID)
	if err != nil {
		slog.ErrorContext(ctx, "node.view_read_failed", slog.String("err", err.Error()), slog.String("node_id", nodeID.String()))
		return nil, fmt.Errorf("read node %s after its mutation: %w", nodeID, err)
	}
	if view == nil {
		return nil, fmt.Errorf("read node %s after its mutation: %w", nodeID, domain.ErrNotFound)
	}
	return view, nil
}

// mergedUpdateProps returns the properties after applying patch and parent to
// stored. A JSON null in patch removes the property.
func mergedUpdateProps(stored, patch map[string]json.RawMessage, parent *node.ParentChange) map[string]json.RawMessage {
	merged := make(map[string]json.RawMessage, len(stored)+len(patch))
	maps.Copy(merged, stored)
	for name, value := range patch {
		if isDeletedPropValue(value) {
			delete(merged, name)
			continue
		}
		merged[name] = value
	}
	if parent != nil {
		merged["parent_id"] = MustRawString(parent.TargetID.String())
	}
	return merged
}

// propsWithoutParent returns a copy of props without parent_id. The plan sets
// the parent through its Parent field.
func propsWithoutParent(props map[string]json.RawMessage) map[string]json.RawMessage {
	patch := make(map[string]json.RawMessage, len(props))
	for name, value := range props {
		if name != "parent_id" {
			patch[name] = value
		}
	}
	return patch
}

func stampRelationshipChanges(changes node.RelationshipChanges, actorID uuid.UUID, changedAt time.Time) node.RelationshipChanges {
	for _, relationship := range changes.Add {
		if relationship.CreatedBy == uuid.Nil {
			relationship.CreatedBy = actorID
		}
		if relationship.CreatedAt.IsZero() {
			relationship.CreatedAt = changedAt
		}
	}
	return changes
}
