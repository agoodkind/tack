package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// UpdateInput specifies optional fields to update.
type UpdateInput struct {
	NodeID              uuid.UUID
	Name                *string
	Props               map[string]json.RawMessage // merged on top of existing
	RelationshipChanges node.RelationshipChanges
	ActorID             uuid.UUID
	// ScopeID, when not [uuid.Nil], replaces the stored scope_id. It requires a
	// change to a different parent in the same update. Internal callers set it
	// when moving a node into another scope. Props cannot set scope_id.
	ScopeID uuid.UUID
}

// Update rewrites a node's name and/or Props. Property index entries for
// indexed props are refreshed when the value changes.
func (s *NodeService) Update(ctx context.Context, in UpdateInput) (*node.NodeView, error) {
	ctx, span := telemetry.StartSpan(
		ctx,
		"service.node.update",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("node.id", in.NodeID.String())),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("node_id", in.NodeID.String()))
	log := telemetry.L(ctx)

	existing, err := s.readUpdateNode(ctx, in.NodeID)
	if err != nil {
		return nil, err
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

	if err := s.validateUpdateProps(ctx, source.OrgID, nt, in.Props, source.Props); err != nil {
		return nil, err
	}

	parent, otherChanges, err := s.parentChangeForUpdate(ctx, snapshot, in.RelationshipChanges)
	if err != nil {
		return nil, err
	}

	props, err := scopedUpdateProps(ctx, in, parent.Change, snapshot.ParentID)
	if err != nil {
		return nil, err
	}

	merged := mergedUpdateProps(source.Props, props)
	// An update can change a value that a reference renders from, such as the
	// scope pointer or numbered property. Update renders reference keys from
	// the merged properties and claims them. The store refuses a move to a
	// claimed reference and does not leave a stale claim. A type without a
	// template does not render keys, and the store keeps the stored claims.
	scopeID := updateScopeID(merged, snapshot.ParentID, parent.Change)
	referenceKeys, err := s.referenceKeysFor(ctx, source.OrgID, nt, scopeID, merged)
	if err != nil {
		return nil, err
	}

	referenceRecords, err := s.referenceGraph(
		ctx,
		source.OrgID,
		schema.Schema,
		setUpdateProps(props),
	)
	if err != nil {
		return nil, err
	}

	plan := node.MutationPlan{
		OrgID:         source.OrgID,
		NodeID:        source.ID,
		NodeType:      source.NodeType,
		ActorID:       in.ActorID,
		Source:        node.SourceExpectation{Absent: false, PrimaryDigest: snapshot.PrimaryDigest},
		MetadataEpoch: schema.MetadataEpoch,
		Graph: node.GraphExpectation{
			Records:       sortedGraphRecords(parent.Records, referenceRecords),
			Relationships: parent.Relationships,
			Incoming:      nil,
		},
		Name:          in.Name,
		Props:         props,
		Parent:        parent.Change,
		ReferenceKeys: referenceKeys,
		Relationships: stampRelationshipChanges(otherChanges, in.ActorID, clock.Now().UTC()),
		Idempotency:   nil,
		Checkpoint:    nil,
	}
	if _, err := s.nodes.ApplyMutation(ctx, plan); err != nil {
		log.ErrorContext(
			ctx,
			"node.update_failed",
			slog.String("node_id", in.NodeID.String()),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("update node: %w", err)
	}

	return s.committedView(ctx, in.NodeID)
}
