package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/telemetry"
)

// ScopeLevel describes one level of the hierarchy between the entry point and
// a leaf type. Slug is the type-derived MCP command token, not a node address.
type ScopeLevel struct {
	TypeKey   string
	Slug      string
	ParamName string // e.g. "project_reference"
}

// Resolver translates human-readable references (workspace address, project
// reference, sequence references) into node UUIDs, using only the generic primitives.
type Resolver struct {
	nodes   node.NodeRepository
	reader  node.NodeReader
	members org.MemberRepository

	entryPointTypeKey string
	entryPointSlug    string
	// entryPointRefProp is the property name used to look up the entry point by
	// human-readable reference (e.g. "slug" for workspace types with
	// ReferenceDirectProperty strategy). Empty when the entry point type does
	// not declare a direct address property.
	entryPointRefProp string
	scopeChain        []ScopeLevel
	sequenceTypeKeys  []string
	typeIndex         map[string]*node.NodeType
}

func NewResolver(nodes node.NodeRepository, reader node.NodeReader, members org.MemberRepository, nodeTypes []*node.NodeType) *Resolver {
	r := &Resolver{
		nodes:             nodes,
		reader:            reader,
		members:           members,
		entryPointTypeKey: "",
		entryPointSlug:    "",
		entryPointRefProp: "",
		scopeChain:        nil,
		sequenceTypeKeys:  nil,
		typeIndex:         nil,
	}
	r.typeIndex = node.BuildTypeIndex(nodeTypes)
	for _, nt := range nodeTypes {
		if nt.Features.Has(node.FeatureIsEntryPoint) {
			r.entryPointTypeKey = nt.TypeKey
			r.entryPointSlug = strings.ToLower(nt.Slug)
			r.entryPointRefProp = nt.Reference.DirectAddressProperty()
		}
		if nt.Features.Has(node.FeatureHasSequenceID) {
			r.sequenceTypeKeys = append(r.sequenceTypeKeys, nt.TypeKey)
		}
	}
	// Build default (longest) scope chain below the entry point.
	for _, nt := range nodeTypes {
		chain := r.ScopeChainForType(nt)
		if len(chain) > len(r.scopeChain) {
			r.scopeChain = chain
		}
	}
	return r
}

func (r *Resolver) EntryPointParamName() string {
	return r.entryPointSlug + referenceParamSuffix
}

// ScopeChainForType returns the ordered scope levels from the entry point down
// to (but not including) nt. Returns nil when nt sits directly under the entry point.
func (r *Resolver) ScopeChainForType(nt *node.NodeType) []ScopeLevel {
	var chain []ScopeLevel
	current := nt
	for len(current.CanLiveUnder) > 0 {
		parentKey := current.CanLiveUnder[0]
		parent := r.typeIndex[parentKey]
		if parent == nil {
			break
		}
		if parent.Features.Has(node.FeatureIsEntryPoint) {
			break
		}
		chain = append(chain, ScopeLevel{
			TypeKey:   parent.TypeKey,
			Slug:      strings.ToLower(parent.Slug),
			ParamName: strings.ToLower(parent.Slug) + referenceParamSuffix,
		})
		current = parent
	}
	// Reverse so top-down.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// Workspace resolves an entry point reference to its NodeView by scanning the
// org-scoped node_by_property index for each org the authenticated user belongs
// to. This replaces the former global address_index lookup, which had no orgID
// component and was the root cause of the 2026-05-09 parallel-org outage.
func (r *Resolver) Workspace(ctx context.Context, reference string) (*node.NodeView, error) {
	if r.entryPointRefProp == "" {
		return nil, fmt.Errorf("%s reference %q: entry point type has no declared reference property: %w",
			r.entryPointSlug, reference, domain.ErrInvalidArgument)
	}
	userID, ok := auth.UserID(ctx)
	if !ok {
		return nil, fmt.Errorf("unauthenticated")
	}
	orgIDs, err := r.members.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("%s reference %q: list orgs: %w", r.entryPointSlug, reference, err)
	}
	for _, rawValue := range referenceLookupValues(reference) {
		for _, orgID := range orgIDs {
			matched, err := r.nodes.ListByProperty(ctx, orgID, r.entryPointTypeKey, r.entryPointRefProp, rawValue)
			if err != nil {
				continue
			}
			for _, n := range matched {
				view, viewErr := r.reader.Get(ctx, n.ID)
				if viewErr != nil || view == nil {
					continue
				}
				stampAuditEntryPoint(ctx, view)
				return view, nil
			}
		}
	}
	return nil, fmt.Errorf("%s reference %q: %w", r.entryPointSlug, reference, domain.ErrNotFound)
}

// ResolveScope looks up a scope node through the node type's reference
// contract, scoped under the given parent.
//
// On miss this is a common debugging scenario in Tack. We log every miss at
// Info with the inputs that produced it so the next miss is diagnosable from
// one line.
func (r *Resolver) ResolveScope(ctx context.Context, parent *node.NodeView, level ScopeLevel, reference string) (*node.NodeView, error) {
	view, err := r.resolveScopeReference(ctx, parent, level, reference)
	if err == nil && view != nil {
		if parent != nil && parent.NodeType == r.entryPointTypeKey {
			stampAuditEntryPoint(ctx, parent)
		}
		audit.SetScopeFields(ctx, audit.Scope{ScopeID: view.ID})
		return view, nil
	}
	telemetry.IncResolverMiss("scope")
	telemetry.L(ctx).InfoContext(ctx, "resolver.scope.miss",
		slog.String("level", level.TypeKey),
		slog.String("reference", reference),
		slog.String("parent_id", parent.ID.String()),
		slog.String("parent_type", parent.NodeType),
		slog.String("err", err.Error()),
	)
	return nil, fmt.Errorf("%s reference %q: %w", level.Slug, reference, domain.ErrNotFound)
}

// WorkspacesForUser returns every workspace the user has access to.
func (r *Resolver) WorkspacesForUser(ctx context.Context, userID uuid.UUID) ([]*node.NodeView, error) {
	orgIDs, err := r.members.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(orgIDs) == 1 {
		stampAuditOrg(ctx, orgIDs[0])
	}
	var all []*node.NodeView
	for _, orgID := range orgIDs {
		views, err := r.reader.List(ctx, node.NodeListQuery{
			OrgID:    orgID,
			NodeType: r.entryPointTypeKey,
		})
		if err != nil {
			continue
		}
		all = append(all, views...)
	}
	return all, nil
}

// ResolveNodeID accepts a UUID, a metadata-declared reference, or a
// sequence-style reference (e.g. "TACK-65"), and returns the node UUID.
func (r *Resolver) ResolveNodeID(ctx context.Context, input string) (uuid.UUID, error) {
	if id, err := uuid.Parse(input); err == nil {
		resolve, resolveErr := r.reader.Resolve(ctx, id)
		if resolveErr == nil {
			stampAuditNodeResolve(ctx, resolve)
		}
		return id, nil
	}
	id, err := r.resolveSequenceNodeID(ctx, input, r.sequenceTypeKeys)
	if err == nil {
		return id, nil
	}
	var invalidArgument error
	if errors.Is(err, domain.ErrInvalidArgument) {
		invalidArgument = err
	}
	telemetry.IncResolverMiss("node_id")
	projectReference, seqID, _ := ParseNodeIdentifier(input)
	telemetry.L(ctx).InfoContext(ctx, "resolver.node_id.miss",
		slog.String("input", input),
		slog.String("project_reference", projectReference),
		slog.Int("sequence_id", seqID),
		slog.String("err", err.Error()),
		slog.Int("scope_chain_depth", len(r.scopeChain)),
		slog.Int("sequence_types_tried", len(r.sequenceTypeKeys)),
	)
	if invalidArgument != nil {
		return uuid.Nil, invalidArgument
	}
	return uuid.Nil, fmt.Errorf("reference %q: %w", input, domain.ErrNotFound)
}
