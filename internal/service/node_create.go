package service

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

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

// Create writes a new node plus its initial relationships. When the NodeType
// declares FeatureHasSequenceID the service allocates a sequence number from
// the parent as the scope, and stamps Props["sequence"]. When the NodeType
// Reference declares a direct address property, the service writes the address
// index from that property value.
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

	in = normalizeCreateInput(in)
	orgID, err := s.resolveOrgFromParent(ctx, in.ParentID)
	if err != nil {
		return nil, err
	}

	existingResult, err := s.lookupExistingCreate(ctx, log, orgID, in)
	if err != nil {
		return nil, err
	}
	if existingResult != nil {
		return existingResult, nil
	}

	nt, err := s.findNodeType(ctx, orgID, in.NodeTypeKey)
	if err != nil {
		return nil, err
	}

	id := uuid.Must(uuid.NewV7())
	now := createTimeFromUUID(id)
	props, err := s.prepareCreateProps(ctx, log, orgID, nt, in)
	if err != nil {
		return nil, err
	}

	n := newCreateNode(orgID, id, nt.TypeKey, in, props, now)
	view := viewFromNode(n)
	relationships := buildCreateRelationships(orgID, id, in, now)
	indexedProps, err := s.indexedPropNames(ctx, orgID, nt)
	if err != nil {
		return nil, err
	}

	createResult, err := s.createNodeAtomic(ctx, log, in, n, view, relationships, indexedProps, now)
	if err != nil {
		return nil, err
	}
	if createResult != nil {
		return createResult, nil
	}

	s.indexCreateSearch(ctx, log, id, view)
	s.createDefaultChildren(ctx, log, nt, id, in.ActorID)

	log.InfoContext(ctx, "node.Create",
		slog.String("node_id", id.String()),
		slog.String("node_type", nt.TypeKey),
	)
	return &CreateResult{View: view, Existed: false}, nil
}
