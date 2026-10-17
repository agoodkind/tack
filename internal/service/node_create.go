package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// firstReferenceKey is the rendered reference a node's ledger row names, or ""
// for a type that renders none.
func firstReferenceKey(keys []node.ReferenceKey) string {
	if len(keys) == 0 {
		return ""
	}
	return keys[0].Encoded
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

// Create writes a new node plus its initial relationships. A primary reference
// template can declare the property generated from its derived counter key.
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

	_, schema, err := s.readMutationSnapshot(ctx, orgID, in.ParentID)
	if err != nil {
		return nil, err
	}
	nt := schema.Types[in.NodeTypeKey]
	if nt == nil {
		return nil, fmt.Errorf("node type %q: %w", in.NodeTypeKey, domain.ErrNotFound)
	}
	id := uuid.Must(uuid.NewV7())
	plan, err := s.createPlan(ctx, log, orgID, id, nt, schema, in)
	if err != nil {
		return nil, err
	}
	// The ledger row commits with the node or not at all: the store writes
	// the staged event inside the create transaction (TACK-173).
	if err := audit.StageStateChange(ctx, audit.VerbNodeCreate, audit.Entity{
		Type: "node", NodeType: nt.TypeKey, ID: id,
		Identifier: firstReferenceKey(plan.ReferenceKeys), Name: in.Name,
	}); err != nil {
		log.ErrorContext(ctx, "node.Create: stage audit row", slog.String("err", err.Error()))
		return nil, fmt.Errorf("stage the audit row for creating %s: %w", id, err)
	}
	receipt, err := s.nodes.ApplyMutation(ctx, *plan)
	if err != nil {
		log.ErrorContext(ctx, "node.create_failed", slog.String("node_type", nt.TypeKey), slog.String("node_id", id.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("create node: %w", err)
	}
	view, err := s.committedView(ctx, receipt.NodeID)
	if err != nil {
		return nil, err
	}
	if receipt.NodeID != id {
		return &CreateResult{View: view, Existed: true}, nil
	}

	s.createDefaultChildren(ctx, log, nt, id, in.ActorID)

	log.InfoContext(ctx, "node.Create",
		slog.String("node_id", id.String()),
		slog.String("node_type", nt.TypeKey),
	)
	return &CreateResult{View: view, Existed: false}, nil
}

// createPlan prepares the checked mutation that creates node id of type nt
// from in.
func (s *NodeService) createPlan(
	ctx context.Context, log *slog.Logger, orgID, id uuid.UUID, nt *node.NodeType, schema node.SchemaSnapshot, in CreateInput,
) (*node.MutationPlan, error) {
	props, err := s.prepareCreateProps(ctx, log, orgID, nt, in)
	if err != nil {
		return nil, err
	}
	referenceKeys, err := s.referenceKeysFor(ctx, orgID, nt, in.ScopeID, props)
	if err != nil {
		return nil, err
	}
	parentRecords, err := s.parentGraph(ctx, orgID, id, in.ParentID)
	if err != nil {
		return nil, err
	}
	referenceRecords, err := s.referenceGraph(ctx, orgID, schema.Schema, props)
	if err != nil {
		return nil, err
	}
	relationships, err := createRelationships(ctx, id, in)
	if err != nil {
		return nil, err
	}
	var parent *node.ParentChange
	if in.ParentID != uuid.Nil {
		parent = &node.ParentChange{TargetID: in.ParentID}
	}
	name := in.Name
	return &node.MutationPlan{
		OrgID: orgID, NodeID: id, NodeType: nt.TypeKey, ActorID: in.ActorID,
		Source: node.SourceExpectation{Absent: true, PrimaryDigest: node.Digest{}}, MetadataEpoch: schema.MetadataEpoch,
		Graph: node.GraphExpectation{Records: sortedGraphRecords(parentRecords, referenceRecords), Relationships: nil, Incoming: nil},
		Name:  &name, Props: propsWithoutParent(props), Parent: parent, ReferenceKeys: referenceKeys,
		Relationships: node.RelationshipChanges{Add: relationships, Remove: nil},
		Idempotency:   createIdempotencyRecord(in, id, clock.Now().UTC()),
	}, nil
}
