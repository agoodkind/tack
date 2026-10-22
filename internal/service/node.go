package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/telemetry"
)

// NodeService is the single service for CRUD and relationship operations on
// every node type. Behavior is driven by Features on the NodeType definition;
// the service does not inspect type names or carry concept-specific fields.
type NodeService struct {
	nodes         node.MutableNodeRepository
	reader        node.NodeReader
	nodeTypes     node.TypeRepository
	propertyDefs  node.PropertyDefRepository
	relationships node.RelationshipRepository
	deleter       node.NodeDeleter
	members       org.MemberRepository
	memberTargets node.MemberTargetAdder
}

func NewNodeService(
	nodes node.MutableNodeRepository,
	reader node.NodeReader,
	nodeTypes node.TypeRepository,
	propertyDefs node.PropertyDefRepository,
	relationships node.RelationshipRepository,
	deleter node.NodeDeleter,
	members org.MemberRepository,
	memberTargets node.MemberTargetAdder,
) *NodeService {
	return &NodeService{
		nodes:         nodes,
		reader:        reader,
		nodeTypes:     nodeTypes,
		propertyDefs:  propertyDefs,
		relationships: relationships,
		deleter:       deleter,
		members:       members,
		memberTargets: memberTargets,
	}
}

// AddRelationship adds a directed relationship from a node to a node or an
// organization member. It returns [node.ErrReservedParentRelation] for a
// child_of relationship. The parent_id field sets the parent.
// It stores a member relationship when the target is a member of rel.OrgID.
// Otherwise, it adds a node relationship.
func (s *NodeService) AddRelationship(ctx context.Context, rel *node.Relationship) error {
	ctx, span := telemetry.StartSpan(ctx, "service.node.relationship.add",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("node.relationship.type", rel.RelationType),
			attribute.String("node.source_id", rel.SourceID.String()),
			attribute.String("node.target_id", rel.TargetID.String()),
		),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx,
		slog.String("relation_type", rel.RelationType),
		slog.String("source_id", rel.SourceID.String()),
		slog.String("target_id", rel.TargetID.String()),
	)
	if rel.RelationType == node.RelChildOf {
		err := fmt.Errorf("add relationship from %s to %s: %w", rel.SourceID, rel.TargetID, node.ErrReservedParentRelation)
		slog.ErrorContext(ctx, "node.relationship.add_refused", slog.String("err", err.Error()))
		return err
	}
	if rel.CreatedAt.IsZero() {
		rel.CreatedAt = clock.Now().UTC()
	}
	if err := audit.StageStateChange(ctx, audit.VerbRelationshipAdd,
		relationshipAuditEntity(rel.SourceID, rel.RelationType, rel.TargetID)); err != nil {
		slog.ErrorContext(ctx, "node.AddRelationship: stage audit row", slog.String("err", err.Error()))
		return fmt.Errorf("stage the audit row for adding %s: %w", rel.RelationType, err)
	}
	memberTarget, err := s.isMemberTarget(ctx, rel)
	if err != nil {
		return err
	}
	if memberTarget {
		if err := s.memberTargets.AddMemberTarget(ctx, rel); err != nil {
			wrapped := fmt.Errorf("add %s relationship from %s to member %s: %w", rel.RelationType, rel.SourceID, rel.TargetID, err)
			slog.ErrorContext(ctx, "node.relationship.add_member_target_failed", slog.String("err", wrapped.Error()))
			return wrapped
		}
		return nil
	}
	return s.relationships.Add(ctx, rel)
}

// relationshipAuditEntity names a relationship in its ledger row: the source
// node is the entity, the relation type its identifier, the target its name.
func relationshipAuditEntity(sourceID uuid.UUID, relationType string, targetID uuid.UUID) audit.Entity {
	return audit.Entity{
		Type: "relationship", NodeType: "", ID: sourceID, Identifier: relationType, Name: targetID.String(),
	}
}

// RemoveRelationship removes a directed edge.
func (s *NodeService) RemoveRelationship(ctx context.Context, orgID, sourceID uuid.UUID, relationType string, targetID uuid.UUID) error {
	ctx, span := telemetry.StartSpan(ctx, "service.node.relationship.remove",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("node.relationship.type", relationType),
			attribute.String("node.source_id", sourceID.String()),
			attribute.String("node.target_id", targetID.String()),
		),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx,
		slog.String("relation_type", relationType),
		slog.String("source_id", sourceID.String()),
		slog.String("target_id", targetID.String()),
	)
	if err := audit.StageStateChange(ctx, audit.VerbRelationshipRemove,
		relationshipAuditEntity(sourceID, relationType, targetID)); err != nil {
		slog.ErrorContext(ctx, "node.RemoveRelationship: stage audit row", slog.String("err", err.Error()))
		return fmt.Errorf("stage the audit row for removing %s: %w", relationType, err)
	}
	return s.relationships.Remove(ctx, orgID, sourceID, relationType, targetID)
}

// resolveOrgFromParent returns the org context for a parent node, handling the
// "no parent" case (seed for org-level nodes).
func (s *NodeService) resolveOrgFromParent(ctx context.Context, parentID uuid.UUID) (uuid.UUID, error) {
	if parentID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("parent_id required: %w", domain.ErrInvalidArgument)
	}
	resolve, err := s.reader.Resolve(ctx, parentID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve parent: %w", err)
	}
	return resolve.OrgID, nil
}
