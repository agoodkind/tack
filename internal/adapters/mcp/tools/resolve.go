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

// ScopeLevel represents one hierarchy level between the entry point and a
// leaf type. Slug supplies the type-derived MCP command token rather than a
// node address.
type ScopeLevel struct {
	TypeKey   string
	Slug      string
	ParamName string // ParamName can be "project_reference".
}

// Resolver converts human-readable references, including workspace
// addresses, project references, and sequence references, into node UUIDs
// with generic storage operations.
type Resolver struct {
	nodes   node.NodeRepository
	reader  node.NodeReader
	members org.MemberRepository

	entryPointTypeKey string
	entryPointSlug    string
	// entryPointRefProp selects the property for entry point reference lookups,
	// such as "slug" for a workspace with the ReferenceDirectProperty strategy.
	// It is empty when the entry point type does not declare a direct address
	// property.
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
	// Build the default scope chain from the longest chain below the entry
	// point.
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

// ScopeChainForType returns the ordered ancestor scope levels between the
// entry point and nt.
//
// It follows the first CanLiveUnder parent at each level and excludes both
// the entry point and nt. A type directly under the entry point returns nil.
// A child of that type returns one scope level.
//
//   - With workspace as the entry point, project under workspace, epic under
//     project, and issue under epic, a call for issue returns the project
//     level followed by the epic level.
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
	// Reverse the levels into ancestor order from the entry point toward nt.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// Workspace resolves an entry point reference to its NodeView within the
// authenticated user's organizations.
//
// It queries the organization-scoped node_by_property index for each
// membership. The former global address_index omitted orgID and caused the
// 2026-05-09 parallel-org outage. Organization-scoped lookup prevents that
// collision.
//
// The lookup requires these inputs:
//
//   - The entry point type declares a reference property.
//   - The request identifies an authenticated user.
//   - The membership repository supplies that user's organizations.
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
				// The property lookup matched a node in one of the caller's
				// organizations. The membership query already checked that organization.
				markAuthorized(ctx)
				stampAuditEntryPoint(ctx, view)
				return view, nil
			}
		}
	}
	return nil, fmt.Errorf("%s reference %q: %w", r.entryPointSlug, reference, domain.ErrNotFound)
}

// ResolveScope looks up a scope node under parent through the node type's
// reference contract.
//
// It logs lookup errors at Info with the level, reference, parent ID, parent
// type, and error for diagnosis.
//
// The result distinguishes these outcomes:
//
//   - A successful lookup returns the node.
//   - An invalid-argument error retains its details, including candidate IDs
//     for an ambiguous reference.
//   - Other lookup errors return not-found.
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
	// Preserve the candidate IDs in an ambiguous-reference error (TACK-476). A
	// not-found error would omit the IDs that the caller needs to choose a
	// reference.
	if errors.Is(err, domain.ErrInvalidArgument) {
		return nil, fmt.Errorf("%s reference %q: %w", level.Slug, reference, err)
	}
	return nil, fmt.Errorf("%s reference %q: %w", level.Slug, reference, domain.ErrNotFound)
}

// WorkspacesForUser lists workspace nodes in the user's member
// organizations.
//
// It queries each organization for the entry point type and skips
// organizations when the list fails.
//
//   - If the user belongs to organizations A and B, the query for A returns
//     workspace X, and the query for B fails, the result contains only
//     workspace X.
func (r *Resolver) WorkspacesForUser(ctx context.Context, userID uuid.UUID) ([]*node.NodeView, error) {
	orgIDs, err := r.members.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	// Each workspace query uses an organization from the user's membership
	// list. Mark the request authorized before reading those workspaces.
	markAuthorized(ctx)
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

// ResolveNodeID returns a node UUID from a UUID or a metadata-enabled
// sequence reference.
//
// A UUID requires membership in the node's organization. Missing and foreign
// nodes return not-found. UUID membership checks can return authentication
// or membership-lookup errors. Invalid sequence arguments retain their
// invalid-argument errors. Other sequence failures return not-found.
//
//   - With a configured sequence type that accepts "TACK-65", a matching
//     reference-key owner in a member organization supplies the returned
//     UUID.
func (r *Resolver) ResolveNodeID(ctx context.Context, input string) (uuid.UUID, error) {
	if id, err := uuid.Parse(input); err == nil {
		return r.resolveMemberNodeID(ctx, id, "node", input)
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

// resolveMemberNodeID resolves a UUID and requires membership in its node's
// organization. Missing nodes and foreign nodes return not-found.
func (r *Resolver) resolveMemberNodeID(ctx context.Context, id uuid.UUID, kind, input string) (uuid.UUID, error) {
	resolve, err := r.reader.Resolve(ctx, id)
	if err != nil || resolve == nil {
		telemetry.IncResolverMiss("node_id")
		slog.WarnContext(ctx, "resolver.node_id.miss",
			slog.String("input", input), slog.String("kind", kind))
		return uuid.Nil, fmt.Errorf("%s %q: %w", kind, input, domain.ErrNotFound)
	}
	if err := r.requireMembership(ctx, resolve.OrgID, kind, input); err != nil {
		return uuid.Nil, err
	}
	stampAuditNodeResolve(ctx, resolve)
	return id, nil
}
