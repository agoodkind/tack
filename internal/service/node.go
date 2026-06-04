package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// NodeService is the single service for CRUD and relationship operations on
// every node type. Behavior is driven by Features on the NodeType definition;
// the service does not inspect type names or carry concept-specific fields.
type NodeService struct {
	nodes         node.NodeRepository
	reader        node.NodeReader
	nodeTypes     node.TypeRepository
	propertyDefs  node.PropertyDefRepository
	relationships node.RelationshipRepository
	deleter       node.NodeDeleter
	searcher      domainsearch.Searcher
}

func NewNodeService(
	nodes node.NodeRepository,
	reader node.NodeReader,
	nodeTypes node.TypeRepository,
	propertyDefs node.PropertyDefRepository,
	relationships node.RelationshipRepository,
	deleter node.NodeDeleter,
	searcher domainsearch.Searcher,
) *NodeService {
	return &NodeService{
		nodes:         nodes,
		reader:        reader,
		nodeTypes:     nodeTypes,
		propertyDefs:  propertyDefs,
		relationships: relationships,
		deleter:       deleter,
		searcher:      searcher,
	}
}

// UpdateInput holds optional fields to update.
type UpdateInput struct {
	NodeID              uuid.UUID
	Name                *string
	Props               map[string]json.RawMessage // merged on top of existing
	RelationshipChanges node.RelationshipChanges
	ActorID             uuid.UUID
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
		return nil, err
	}
	if existing == nil {
		return nil, domain.ErrNotFound
	}

	// Merge Props.
	merged := make(map[string]json.RawMessage, len(existing.Props)+len(in.Props))
	for k, v := range existing.Props {
		merged[k] = v
	}
	for k, v := range in.Props {
		if isDeletedPropValue(v) {
			delete(merged, k)
			continue
		}
		merged[k] = v
	}

	name := existing.Name
	if in.Name != nil {
		name = *in.Name
	}

	now := clock.Now().UTC()
	n := &node.Node{
		ID:        existing.ID,
		OrgID:     existing.OrgID,
		NodeType:  existing.NodeType,
		Name:      name,
		Props:     merged,
		CreatedBy: existing.CreatedBy,
		UpdatedBy: in.ActorID,
		CreatedAt: existing.CreatedAt,
		UpdatedAt: now,
	}
	view := viewFromNode(n)

	nt, err := s.findNodeType(ctx, existing.OrgID, existing.NodeType)
	if err != nil {
		return nil, err
	}
	if err := s.validateUpdateProps(ctx, existing.OrgID, nt, in.Props); err != nil {
		return nil, err
	}
	indexedProps, err := s.indexedPropNames(ctx, existing.OrgID, nt)
	if err != nil {
		return nil, err
	}
	relationshipChanges := stampRelationshipChanges(in.RelationshipChanges, in.ActorID, now)
	if err := s.nodes.UpdateAtomic(ctx, n, view, existing.Props, indexedProps, relationshipChanges); err != nil {
		log.Error("node.Update",
			slog.String("node_id", in.NodeID.String()),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("update node: %w", err)
	}

	if err := s.searcher.Index(ctx, "nodes", in.NodeID.String(), searchDocFromView(view)); err != nil {
		log.Warn("node.Update: search index", slog.String("err", err.Error()))
	}
	return view, nil
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

// Delete removes a node and every FDB record keyed by its ID.
func (s *NodeService) Delete(ctx context.Context, nodeID, actorID uuid.UUID) error {
	ctx, span := telemetry.StartSpan(ctx, "service.node.delete",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("node.id", nodeID.String())),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("node_id", nodeID.String()))
	log := telemetry.L(ctx)

	existing, err := s.reader.Get(ctx, nodeID)
	if err != nil {
		return err
	}
	if existing == nil {
		return domain.ErrNotFound
	}

	if err := s.deleter.DeleteNode(ctx, existing.OrgID, nodeID); err != nil {
		log.Error("node.Delete", slog.String("node_id", nodeID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("delete node: %w", err)
	}

	if err := s.searcher.Delete(ctx, "nodes", nodeID.String()); err != nil {
		log.Warn("node.Delete: search removal", slog.String("err", err.Error()))
	}
	return nil
}

// AddRelationship attaches a directed edge between two nodes.
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
	if rel.CreatedAt.IsZero() {
		rel.CreatedAt = clock.Now().UTC()
	}
	return s.relationships.Add(ctx, rel)
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

// findNodeType resolves a NodeType by TypeKey within an org.
func (s *NodeService) findNodeType(ctx context.Context, orgID uuid.UUID, key string) (*node.NodeType, error) {
	nts, err := s.nodeTypes.List(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list node types: %w", err)
	}
	for _, nt := range nts {
		if nt.TypeKey == key {
			return nt, nil
		}
	}
	return nil, fmt.Errorf("node type %q: %w", key, domain.ErrNotFound)
}

// indexedPropNames returns the names of PropertyDefs in the org that apply to
// this NodeType and have Indexed=true.
func (s *NodeService) indexedPropNames(ctx context.Context, orgID uuid.UUID, nt *node.NodeType) ([]string, error) {
	defs, err := s.propertyDefs.List(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list property defs: %w", err)
	}
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		if !d.Indexed {
			continue
		}
		if !appliesTo(d, nt) {
			continue
		}
		out = append(out, d.Name)
	}
	return out, nil
}

// appliesTo reports whether a PropertyDef applies to a NodeType. Empty
// AppliesToFeatures means "all types".
func appliesTo(d *node.PropertyDef, nt *node.NodeType) bool {
	if len(d.AppliesToFeatures) == 0 {
		return true
	}
	return nt.Features.HasAny(d.AppliesToFeatures...)
}

// viewFromNode materializes the NodeView snapshot from a Node. Every write path
// that updates the primary also refreshes the view; keeping them in the same
// shape means the view construction is trivial.
func viewFromNode(n *node.Node) *node.NodeView {
	// Copy Props so the view and the node don't share the same map.
	props := make(map[string]json.RawMessage, len(n.Props))
	for k, v := range n.Props {
		props[k] = v
	}
	return &node.NodeView{
		ID:        n.ID,
		OrgID:     n.OrgID,
		NodeType:  n.NodeType,
		Name:      n.Name,
		Props:     props,
		CreatedBy: n.CreatedBy,
		UpdatedBy: n.UpdatedBy,
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
	}
}

// searchDocFromView builds a generic search document. The doc contains the
// universal fields plus the raw JSON Props map; the search adapter indexes
// the JSON values directly so it can filter on any indexed property without
// the service layer having to know which props are filterable.
func searchDocFromView(v *node.NodeView) *domainsearch.NodeDoc {
	doc := &domainsearch.NodeDoc{
		ID:       v.ID.String(),
		OrgID:    v.OrgID.String(),
		NodeType: v.NodeType,
		Name:     v.Name,
	}
	if len(v.Props) > 0 {
		doc.Props = make(map[string]json.RawMessage, len(v.Props))
		for k, raw := range v.Props {
			doc.Props[k] = raw
		}
	}
	return doc
}
