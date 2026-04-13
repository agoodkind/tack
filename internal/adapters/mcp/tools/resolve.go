package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/telemetry"
)

// Resolver translates human-readable identifiers to workspace/project context.
// Type keys are precomputed from Features at construction time -- no hardcoded
// type name constants anywhere in this file.
// ScopeLevel describes one level of the hierarchy between the entry point and
// leaf types. Each level has a type key and a slug used to derive MCP param names.
type ScopeLevel struct {
	TypeKey   string
	Slug      string
	ParamName string // e.g. "project_identifier"
}

type Resolver struct {
	entities node.EntityRepository
	reader   node.NodeReader
	members  org.MemberRepository

	// Precomputed from NodeType Features at construction time.
	entryPointTypeKey  string       // type with FeatureIsEntryPoint
	entryPointSlug     string       // slug of the entry point type
	orgTypeKey         string       // depth-0 root type
	scopeChain         []ScopeLevel // ordered scope levels below entry point (e.g. [project] or [team, project])
	sequenceTypeKeys   []string     // types with FeatureHasSequenceID
	assignableTypeKeys []string     // types with FeatureHasAssignees
}

// EntryPointParamName returns the MCP parameter name for the entry point
// identifier, derived from the entry point type's slug (e.g. "workspace_slug").
func (r *Resolver) EntryPointParamName() string {
	return r.entryPointSlug + "_slug"
}

// ScopeChainForType returns the ordered scope levels needed to reach a given
// node type from the entry point. For a type at depth 3 (e.g. issue under
// project under workspace), this returns [project]. For depth 4 it might
// return [team, project]. Returns nil for types directly under the entry point.
func (r *Resolver) ScopeChainForType(nt *node.NodeType, typeIndex map[string]*node.NodeType) []ScopeLevel {
	// Walk CanLiveUnder upward, collecting scope types until we hit the entry point.
	var chain []ScopeLevel
	current := nt
	for {
		if len(current.CanLiveUnder) == 0 {
			break
		}
		parentKey := current.CanLiveUnder[0] // take first parent
		parent := typeIndex[parentKey]
		if parent == nil {
			break
		}
		if parent.Features.Has(node.FeatureIsEntryPoint) {
			break // reached the entry point, stop
		}
		chain = append(chain, ScopeLevel{
			TypeKey:   parent.TypeKey,
			Slug:      strings.ToLower(parent.Slug),
			ParamName: strings.ToLower(parent.Slug) + "_identifier",
		})
		current = parent
	}
	// Reverse so it's top-down (closest to entry point first).
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// NewResolver creates a Resolver backed by the generic node system.
// nodeTypes is used to precompute type keys from Features so that no runtime
// code references hardcoded type name constants.
func NewResolver(entities node.EntityRepository, reader node.NodeReader, members org.MemberRepository, nodeTypes []*node.NodeType) *Resolver {
	r := &Resolver{entities: entities, reader: reader, members: members}
	typeIndex := node.BuildTypeIndex(nodeTypes)
	for _, nt := range nodeTypes {
		if nt.Features.Has(node.FeatureIsEntryPoint) {
			r.entryPointTypeKey = nt.TypeKey
			r.entryPointSlug = strings.ToLower(nt.Slug)
		}
		if nt.Features.Has(node.FeatureIsScope) && node.HierarchyDepth(nt, typeIndex) == 0 {
			r.orgTypeKey = nt.TypeKey
		}
		if nt.Features.Has(node.FeatureHasSequenceID) {
			r.sequenceTypeKeys = append(r.sequenceTypeKeys, nt.TypeKey)
		}
		if nt.Features.Has(node.FeatureHasAssignees) {
			r.assignableTypeKeys = append(r.assignableTypeKeys, nt.TypeKey)
		}
	}
	// Build the default scope chain from the longest path below entry point.
	// This is used by ResolveNodeID and other callers that don't have a specific type.
	for _, nt := range nodeTypes {
		chain := r.ScopeChainForType(nt, typeIndex)
		if len(chain) > len(r.scopeChain) {
			r.scopeChain = chain
		}
	}
	return r
}

// Workspace resolves an entry point slug to its NodeListView.
func (r *Resolver) Workspace(ctx context.Context, slug string) (*node.NodeListView, error) {
	log := telemetry.L(ctx)
	id, err := r.entities.GetBySlug(ctx, r.entryPointTypeKey, slug)
	if err != nil {
		log.Warn("resolve entry point: slug lookup failed", slog.String("slug", slug), slog.String("err", err.Error()))
		return nil, fmt.Errorf("%s %q: %w", r.entryPointSlug, slug, err)
	}
	if id == uuid.Nil {
		log.Warn("resolve entry point: not found", slog.String("slug", slug))
		return nil, fmt.Errorf("%s %q: %w", r.entryPointSlug, slug, domain.ErrNotFound)
	}
	view, err := r.reader.Get(ctx, id)
	if err != nil {
		log.Warn("resolve entry point: get view failed", slog.String("slug", slug), slog.String("err", err.Error()))
		return nil, fmt.Errorf("%s %q: %w", r.entryPointSlug, slug, err)
	}
	if view == nil {
		return nil, fmt.Errorf("%s %q: %w", r.entryPointSlug, slug, domain.ErrNotFound)
	}
	return view, nil
}

// ResolveScope resolves a scope container by identifier within a parent.
// The scope type is determined by the ScopeLevel. Returns the parent view and
// the resolved scope view.
func (r *Resolver) ResolveScope(ctx context.Context, parent *node.NodeListView, level ScopeLevel, identifier string) (*node.NodeListView, error) {
	log := telemetry.L(ctx)
	identPropID := node.SystemPropID(parent.ID, "identifier")
	nvs, err := r.entities.ListByProperty(ctx, parent.OrgID, parent.ID, level.TypeKey, identPropID, node.TextPropertyValue(strings.ToUpper(identifier)))
	if err != nil {
		log.Warn("resolve scope: property lookup failed",
			slog.String("type", level.TypeKey),
			slog.String("identifier", identifier),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("%s %q: %w", level.Slug, identifier, err)
	}
	if len(nvs) == 0 {
		log.Warn("resolve scope: not found",
			slog.String("type", level.TypeKey),
			slog.String("identifier", identifier),
		)
		return nil, fmt.Errorf("%s %q: %w", level.Slug, identifier, domain.ErrNotFound)
	}
	view, err := r.reader.Get(ctx, nvs[0].ID)
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", level.Slug, identifier, err)
	}
	if view == nil {
		return nil, fmt.Errorf("%s %q: %w", level.Slug, identifier, domain.ErrNotFound)
	}
	return view, nil
}

// Project resolves an entry point slug + scope identifier to their views.
// This is a convenience wrapper over Workspace + ResolveScope for the first
// scope level in the chain. Works for any hierarchy depth.
func (r *Resolver) Project(ctx context.Context, entryPointSlug, scopeIdentifier string) (*node.NodeListView, *node.NodeListView, error) {
	ws, err := r.Workspace(ctx, entryPointSlug)
	if err != nil {
		return nil, nil, err
	}
	if len(r.scopeChain) == 0 {
		return nil, nil, fmt.Errorf("no scope types defined: %w", domain.ErrNotFound)
	}
	// Resolve the last scope level (deepest container, e.g. project).
	// For a single-level chain this is r.scopeChain[0].
	// For multi-level, the caller should use ResolveChain instead.
	last := r.scopeChain[len(r.scopeChain)-1]
	scope, err := r.ResolveScope(ctx, ws, last, scopeIdentifier)
	if err != nil {
		return nil, nil, err
	}
	return ws, scope, nil
}

// WorkspacesForUser returns all workspace NodeListViews accessible to the given user.
func (r *Resolver) WorkspacesForUser(ctx context.Context, userID uuid.UUID) ([]*node.NodeListView, error) {
	orgIDs, err := r.members.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	var all []*node.NodeListView
	for _, orgID := range orgIDs {
		views, err := r.reader.List(ctx, node.NodeListQuery{
			OrgID:       orgID,
			WorkspaceID: uuid.Nil,
			NodeType:    r.entryPointTypeKey,
		})
		if err != nil {
			continue
		}
		all = append(all, views...)
	}
	return all, nil
}

// OrgBySlug resolves an org slug to its NodeListView.
func (r *Resolver) OrgBySlug(ctx context.Context, slug string) (*node.NodeListView, error) {
	orgID, err := r.entities.GetBySlug(ctx, r.orgTypeKey, slug)
	if err != nil {
		return nil, fmt.Errorf("org %q: %w", slug, err)
	}
	if orgID == uuid.Nil {
		return nil, fmt.Errorf("org %q: %w", slug, domain.ErrNotFound)
	}
	view, err := r.reader.Get(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("org %q: %w", slug, err)
	}
	if view == nil {
		return nil, fmt.Errorf("org %q: %w", slug, domain.ErrNotFound)
	}
	return view, nil
}

// ViewSlug extracts the slug string from a NodeListView's CustomProps.
func ViewSlug(v *node.NodeListView) string {
	if v == nil || v.CustomProps == nil {
		return ""
	}
	raw, ok := v.CustomProps["slug"]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// ViewIdentifier extracts the identifier string from a NodeListView's CustomProps.
func ViewIdentifier(v *node.NodeListView) string {
	if v == nil || v.CustomProps == nil {
		return ""
	}
	raw, ok := v.CustomProps["identifier"]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// ResolveNodeID accepts either a raw UUID or a human-readable identifier (e.g. "TACK-65")
// and returns the resolved node UUID. For identifiers, it looks up the project by
// identifier prefix, then resolves the sequence number within that project.
func (r *Resolver) ResolveNodeID(ctx context.Context, input string) (uuid.UUID, error) {
	// Try UUID first.
	if id, err := uuid.Parse(input); err == nil {
		return id, nil
	}
	// Try identifier format: PROJECT-N
	projectIdent, seqID, err := ParseNodeIdentifier(input)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid node_id %q: must be a UUID or identifier like TACK-65", input)
	}
	log := telemetry.L(ctx)
	log.Debug("resolve node by identifier",
		slog.String("project_ident", projectIdent),
		slog.Int("seq_id", seqID),
	)
	// Find the workspace and project by iterating user's workspaces.
	userID, ok := auth.UserID(ctx)
	if !ok {
		return uuid.Nil, fmt.Errorf("unauthenticated")
	}
	wss, err := r.WorkspacesForUser(ctx, userID)
	if err != nil {
		return uuid.Nil, err
	}
	for _, ws := range wss {
		_, proj, err := r.Project(ctx, ViewSlug(ws), projectIdent)
		if err != nil {
			continue
		}
		for _, typeKey := range r.sequenceTypeKeys {
			nodeID, err := r.entities.GetBySequence(ctx, ws.OrgID, proj.ID, typeKey, int64(seqID))
			if err != nil || nodeID == uuid.Nil {
				continue
			}
			return nodeID, nil
		}
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
