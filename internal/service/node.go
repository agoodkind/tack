package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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

// CreateInput holds the arguments for Create. Relationships lets callers attach
// edges (assigned_to, labeled_with, etc.) in the same FDB transaction as the
// primary write.
type CreateInput struct {
	// ParentID is the direct container the new node lives under. It becomes
	// Props["parent_id"] and the target of the implicit child_of edge.
	// Defaults to ScopeID when zero.
	ParentID uuid.UUID
	// ScopeID is the resolved scope-bearing ancestor. For an issue this is
	// the project, even when ParentID is a deeper container like an epic.
	// Used to allocate sequence numbers so identifiers stay project-wide.
	// Defaults to ParentID when zero.
	ScopeID       uuid.UUID
	NodeTypeKey   string
	Name          string
	Props         map[string]json.RawMessage
	Relationships []*node.Relationship
	ActorID       uuid.UUID
	// IdempotencyKey is optional. When non-empty, Create checks whether a
	// previous successful call stamped this key under the same org. A hit
	// short-circuits and returns the existing node with Existed=true. A miss
	// stamps the key as part of the create transaction.
	IdempotencyKey         string
	IdempotencyFingerprint string
	IdempotencySource      string
}

// CreateResult is the outcome of Create. View is always populated; Existed is
// true when an idempotency-key match short-circuited the create.
type CreateResult struct {
	View    *node.NodeView
	Existed bool
}

const mcpIdempotencyWindow = 24 * time.Hour

// Create writes a new node plus its initial relationships. When the NodeType
// declares FeatureHasSequenceID the service allocates a sequence number from
// the parent as the scope, and stamps Props["sequence"]. When the NodeType
// declares FeatureHasSlug the service writes the global slug index from
// Props["slug"] (or "identifier").
//
// When IdempotencyKey is non-empty, Create first looks up an existing
// (orgID, key) record. If a node already exists under that key, Create
// returns it with CreateResult.Existed=true and performs no writes. If no
// record exists, Create stamps the key alongside the new node.
func (s *NodeService) Create(ctx context.Context, in CreateInput) (*CreateResult, error) {
	ctx, span := telemetry.StartSpan(ctx, "service.node.create",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String("node.type", in.NodeTypeKey)),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("node_type", in.NodeTypeKey))
	log := telemetry.L(ctx)

	// Default unset scope/parent to whichever one is set.
	if in.ScopeID == uuid.Nil {
		in.ScopeID = in.ParentID
	}
	if in.ParentID == uuid.Nil {
		in.ParentID = in.ScopeID
	}

	orgID, err := s.resolveOrgFromParent(ctx, in.ParentID)
	if err != nil {
		return nil, err
	}

	// Idempotency short-circuit: if we already stamped this key, return the
	// node we returned the first time. No new writes.
	if in.IdempotencyKey != "" {
		existingRecord, err := s.nodes.LookupIdempotencyKey(ctx, orgID, in.IdempotencyKey)
		if err != nil {
			return nil, fmt.Errorf("lookup idempotency key: %w", err)
		}
		if existingRecord != nil && !idempotencyRecordExpired(existingRecord) {
			if idempotencyFingerprintConflict(existingRecord.Fingerprint, in.IdempotencyFingerprint) {
				return nil, fmt.Errorf("idempotency key %q reused with different payload: %w", in.IdempotencyKey, domain.ErrConflict)
			}
			view, err := s.reader.Get(ctx, existingRecord.NodeID)
			if err != nil {
				return nil, fmt.Errorf("get existing idempotent node: %w", err)
			}
			if view != nil {
				return &CreateResult{View: view, Existed: true}, nil
			}
			// Sentinel exists but the node is gone. Fall through and
			// re-create; the sentinel will be overwritten with the new ID.
		}
	}

	nt, err := s.findNodeType(ctx, orgID, in.NodeTypeKey)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	id := uuid.Must(uuid.NewV7())

	// Copy Props so we can stamp derived values without mutating the caller's map.
	props := make(map[string]json.RawMessage, len(in.Props)+2)
	for k, v := range in.Props {
		props[k] = v
	}

	// Always record parent_id for fast direct-child lookups without a
	// relationship scan. The parent relationship itself is written below.
	if in.ParentID != uuid.Nil {
		raw, _ := json.Marshal(in.ParentID.String())
		props["parent_id"] = raw
	}

	// Always record scope_id so the resolver can find a sequence-numbered
	// node by its identifier (PROJECT-N) even when the node lives several
	// levels below the project (e.g. an issue under an epic). When ScopeID
	// equals ParentID this is just a duplicate, but stamping it
	// unconditionally keeps the resolver path uniform.
	if in.ScopeID != uuid.Nil {
		raw, _ := json.Marshal(in.ScopeID.String())
		props["scope_id"] = raw
	}

	// FeatureHasSequenceID: allocate a counter under ScopeID, not ParentID.
	// An issue parented under an epic still allocates its sequence from the
	// surrounding project so identifiers stay project-wide.
	if nt.Features.Has(node.FeatureHasSequenceID) && in.ScopeID != uuid.Nil {
		seq, err := s.nodes.AllocateSequence(ctx, orgID, in.ScopeID, nt.TypeKey)
		if err != nil {
			return nil, fmt.Errorf("allocate sequence: %w", err)
		}
		raw, _ := json.Marshal(seq)
		props["sequence"] = raw
	}

	if err := s.applyCreatePropertyDefaults(ctx, orgID, nt, in.ScopeID, props); err != nil {
		return nil, err
	}
	if err := s.validateCreateProps(ctx, orgID, nt, props); err != nil {
		return nil, err
	}

	n := &node.Node{
		ID:        id,
		OrgID:     orgID,
		NodeType:  nt.TypeKey,
		Name:      in.Name,
		Props:     props,
		CreatedBy: in.ActorID,
		UpdatedBy: in.ActorID,
		CreatedAt: now,
		UpdatedAt: now,
	}

	view := viewFromNode(n)

	// Always add a child_of relationship when a parent is supplied.
	rels := make([]*node.Relationship, 0, len(in.Relationships)+1)
	if in.ParentID != uuid.Nil {
		rels = append(rels, &node.Relationship{
			OrgID:        orgID,
			SourceID:     id,
			RelationType: node.RelChildOf,
			TargetID:     in.ParentID,
			CreatedBy:    in.ActorID,
			CreatedAt:    now,
		})
	}
	for _, rel := range in.Relationships {
		if rel.OrgID == uuid.Nil {
			rel.OrgID = orgID
		}
		if rel.SourceID == uuid.Nil {
			rel.SourceID = id
		}
		if rel.CreatedAt.IsZero() {
			rel.CreatedAt = now
		}
		if rel.CreatedBy == uuid.Nil {
			rel.CreatedBy = in.ActorID
		}
		rels = append(rels, rel)
	}

	indexedProps, err := s.indexedPropNames(ctx, orgID, nt)
	if err != nil {
		return nil, err
	}

	idempotencyRecord := createIdempotencyRecord(in, id, now)
	if err := s.nodes.CreateAtomic(ctx, n, view, rels, indexedProps, idempotencyRecord); err != nil {
		if errors.Is(err, domain.ErrConflict) && in.IdempotencyKey != "" {
			existing, lookupErr := s.nodes.LookupIdempotencyKey(ctx, orgID, in.IdempotencyKey)
			if lookupErr != nil {
				return nil, fmt.Errorf("lookup idempotency after create conflict: %w", lookupErr)
			}
			if existing != nil && !idempotencyFingerprintConflict(existing.Fingerprint, in.IdempotencyFingerprint) {
				existingView, getErr := s.reader.Get(ctx, existing.NodeID)
				if getErr != nil {
					return nil, fmt.Errorf("get existing idempotent node after conflict: %w", getErr)
				}
				if existingView != nil {
					return &CreateResult{View: existingView, Existed: true}, nil
				}
			}
		}
		log.Error("node.Create: atomic write",
			slog.String("node_type", nt.TypeKey),
			slog.String("node_id", id.String()),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("create node: %w", err)
	}

	// FeatureHasSlug: register global slug index.
	if nt.Features.Has(node.FeatureHasSlug) {
		slug := firstStringProp(props, "slug", "identifier")
		if slug != "" {
			if err := s.nodes.WriteSlug(ctx, nt.TypeKey, slug, id); err != nil {
				log.Warn("node.Create: write slug", slog.String("slug", slug), slog.String("err", err.Error()))
			}
		}
	}

	// Search index.
	if err := s.searcher.Index(ctx, "nodes", id.String(), searchDocFromView(view)); err != nil {
		log.Warn("node.Create: search index", slog.String("err", err.Error()))
	}

	// Default children: when this NodeType declares DefaultChildren, fan out
	// one Create per child under the node we just created. Failures are
	// logged but never fatal; a partial set of defaults is better than
	// rolling back the parent. Default children are resolved by TypeKey
	// against the same org so the seed's spelling stays the source of truth.
	for _, dc := range nt.DefaultChildren {
		childProps := make(map[string]json.RawMessage, len(dc.Props))
		for k, v := range dc.Props {
			childProps[k] = v
		}
		if _, err := s.Create(ctx, CreateInput{
			ParentID:    id,
			ScopeID:     id,
			NodeTypeKey: dc.TypeKey,
			Name:        dc.Name,
			Props:       childProps,
			ActorID:     in.ActorID,
		}); err != nil {
			log.Warn("node.Create: default child",
				slog.String("parent_type", nt.TypeKey),
				slog.String("child_type", dc.TypeKey),
				slog.String("child_name", dc.Name),
				slog.String("err", err.Error()),
			)
		}
	}

	log.Info("node.Create",
		slog.String("node_id", id.String()),
		slog.String("node_type", nt.TypeKey),
	)
	return &CreateResult{View: view, Existed: false}, nil
}

func createIdempotencyRecord(in CreateInput, nodeID uuid.UUID, createdAt time.Time) *node.IdempotencyRecord {
	if in.IdempotencyKey == "" {
		return nil
	}
	return &node.IdempotencyRecord{
		Key:         in.IdempotencyKey,
		NodeID:      nodeID,
		Fingerprint: in.IdempotencyFingerprint,
		CreatedAt:   createdAt,
		Source:      in.IdempotencySource,
	}
}

func idempotencyFingerprintConflict(existing, incoming string) bool {
	return existing != "" && incoming != "" && existing != incoming
}

func idempotencyRecordExpired(record *node.IdempotencyRecord) bool {
	if record.Source != "mcp" || record.CreatedAt.IsZero() {
		return false
	}
	return time.Since(record.CreatedAt) > mcpIdempotencyWindow
}

// UpdateInput holds optional fields to update.
type UpdateInput struct {
	NodeID  uuid.UUID
	Name    *string
	Props   map[string]json.RawMessage // merged on top of existing
	ActorID uuid.UUID
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
		if len(v) == 0 || string(v) == "null" {
			delete(merged, k)
			continue
		}
		merged[k] = v
	}

	name := existing.Name
	if in.Name != nil {
		name = *in.Name
	}

	now := time.Now().UTC()
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
	if err := s.validateUpdateProps(ctx, existing.OrgID, nt, merged); err != nil {
		return nil, err
	}
	indexedProps, err := s.indexedPropNames(ctx, existing.OrgID, nt)
	if err != nil {
		return nil, err
	}
	if err := s.nodes.UpdateAtomic(ctx, n, view, existing.Props, indexedProps); err != nil {
		log.Error("node.Update",
			slog.String("node_id", in.NodeID.String()),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("update node: %w", err)
	}

	// Reconcile the global slug index when a HasSlug node's slug or
	// identifier changed. The slug index lives outside (orgID, nodeType,
	// nodeID) keying, so UpdateAtomic alone cannot move it.
	if nt.Features.Has(node.FeatureHasSlug) {
		oldSlug := firstStringProp(existing.Props, "slug", "identifier")
		newSlug := firstStringProp(merged, "slug", "identifier")
		if oldSlug != newSlug {
			if oldSlug != "" {
				if err := s.nodes.DeleteSlug(ctx, nt.TypeKey, oldSlug); err != nil {
					log.Warn("node.Update: delete old slug",
						slog.String("slug", oldSlug),
						slog.String("err", err.Error()),
					)
				}
			}
			if newSlug != "" {
				if err := s.nodes.WriteSlug(ctx, nt.TypeKey, newSlug, n.ID); err != nil {
					log.Warn("node.Update: write new slug",
						slog.String("slug", newSlug),
						slog.String("err", err.Error()),
					)
				}
			}
		}
	}

	if err := s.searcher.Index(ctx, "nodes", in.NodeID.String(), searchDocFromView(view)); err != nil {
		log.Warn("node.Update: search index", slog.String("err", err.Error()))
	}
	return view, nil
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
		rel.CreatedAt = time.Now().UTC()
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

// findNodeType resolves a NodeType by TypeKey or Slug within an org.
func (s *NodeService) findNodeType(ctx context.Context, orgID uuid.UUID, key string) (*node.NodeType, error) {
	nts, err := s.nodeTypes.List(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list node types: %w", err)
	}
	for _, nt := range nts {
		if nt.TypeKey == key || nt.Slug == key {
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

// firstStringProp returns the first non-empty string value from Props at any
// of the listed keys.
func firstStringProp(props map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := props[k]
		if !ok {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
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
