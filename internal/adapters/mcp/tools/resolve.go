package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/telemetry"
)

// ScopeLevel describes one level of the hierarchy between the entry point and
// a leaf type. Each level has a type key and a slug used to derive MCP param names.
type ScopeLevel struct {
	TypeKey   string
	Slug      string
	ParamName string // e.g. "project_identifier"
}

// Resolver translates human-readable identifiers (workspace slug, project
// identifier, sequence IDs) into node UUIDs, using only the generic primitives.
type Resolver struct {
	nodes   node.NodeRepository
	reader  node.NodeReader
	members org.MemberRepository

	entryPointTypeKey string
	entryPointSlug    string
	scopeChain        []ScopeLevel
	sequenceTypeKeys  []string
	typeIndex         map[string]*node.NodeType
}

func NewResolver(nodes node.NodeRepository, reader node.NodeReader, members org.MemberRepository, nodeTypes []*node.NodeType) *Resolver {
	r := &Resolver{nodes: nodes, reader: reader, members: members}
	r.typeIndex = node.BuildTypeIndex(nodeTypes)
	for _, nt := range nodeTypes {
		if nt.Features.Has(node.FeatureIsEntryPoint) {
			r.entryPointTypeKey = nt.TypeKey
			r.entryPointSlug = strings.ToLower(nt.Slug)
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
	return r.entryPointSlug + "_slug"
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
			ParamName: strings.ToLower(parent.Slug) + "_identifier",
		})
		current = parent
	}
	// Reverse so top-down.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// Workspace resolves an entry point slug to its NodeView.
func (r *Resolver) Workspace(ctx context.Context, slug string) (*node.NodeView, error) {
	id, err := r.nodes.GetSlug(ctx, r.entryPointTypeKey, slug)
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", r.entryPointSlug, slug, err)
	}
	if id == uuid.Nil {
		return nil, fmt.Errorf("%s %q: %w", r.entryPointSlug, slug, domain.ErrNotFound)
	}
	view, err := r.reader.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, fmt.Errorf("%s %q: %w", r.entryPointSlug, slug, domain.ErrNotFound)
	}
	audit.SetScopeFields(ctx, audit.Scope{
		OrgID:       view.OrgID,
		WorkspaceID: view.ID,
	})
	return view, nil
}

// ResolveScope looks up a scope node through the node type's reference
// contract, scoped under the given parent.
//
// On miss this is a common debugging scenario in Tack. We log every miss at
// Info with the inputs that produced it so the next miss is diagnosable from
// one line.
func (r *Resolver) ResolveScope(ctx context.Context, parent *node.NodeView, level ScopeLevel, identifier string) (*node.NodeView, error) {
	view, err := r.resolveScopeReference(ctx, parent, level, identifier)
	if err == nil && view != nil {
		audit.SetScopeFields(ctx, audit.Scope{ScopeID: view.ID})
		return view, nil
	}
	telemetry.IncResolverMiss("scope")
	telemetry.L(ctx).InfoContext(ctx, "resolver.scope.miss",
		slog.String("level", level.TypeKey),
		slog.String("identifier", identifier),
		slog.String("parent_id", parent.ID.String()),
		slog.String("parent_type", parent.NodeType),
		slog.String("err", err.Error()),
	)
	return nil, fmt.Errorf("%s %q: %w", level.Slug, identifier, domain.ErrNotFound)
}

// WorkspacesForUser returns every workspace the user has access to.
func (r *Resolver) WorkspacesForUser(ctx context.Context, userID uuid.UUID) ([]*node.NodeView, error) {
	orgIDs, err := r.members.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		return nil, err
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

// ResolveNodeID accepts a UUID, a slug-resolvable identifier, or a sequence-style
// identifier (e.g. "TACK-65"), and returns the node UUID.
func (r *Resolver) ResolveNodeID(ctx context.Context, input string) (uuid.UUID, error) {
	if id, err := uuid.Parse(input); err == nil {
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
	projIdent, seqID, _ := ParseNodeIdentifier(input)
	telemetry.L(ctx).InfoContext(ctx, "resolver.node_id.miss",
		slog.String("input", input),
		slog.String("project_ident", projIdent),
		slog.Int("sequence_id", seqID),
		slog.String("err", err.Error()),
		slog.Int("scope_chain_depth", len(r.scopeChain)),
		slog.Int("sequence_types_tried", len(r.sequenceTypeKeys)),
	)
	if invalidArgument != nil {
		return uuid.Nil, invalidArgument
	}
	return uuid.Nil, fmt.Errorf("identifier %q: %w", input, domain.ErrNotFound)
}

// ParseNodeIdentifier splits "ENG-42" into ("ENG", 42).
func ParseNodeIdentifier(identifier string) (projectIdent string, seqID int, err error) {
	idx := strings.LastIndex(identifier, "-")
	if idx <= 0 || idx == len(identifier)-1 {
		return "", 0, fmt.Errorf("invalid identifier %q: expected PROJECT-N format (e.g. ENG-42)", identifier)
	}
	seq, convErr := strconv.Atoi(identifier[idx+1:])
	if convErr != nil || seq <= 0 {
		return "", 0, fmt.Errorf("invalid identifier %q: sequence must be a positive integer", identifier)
	}
	return identifier[:idx], seq, nil
}
