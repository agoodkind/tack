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

	entryPointTypeKey  string
	entryPointSlug     string
	scopeChain         []ScopeLevel
	sequenceTypeKeys   []string
	typeIndex          map[string]*node.NodeType
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
	for {
		if len(current.CanLiveUnder) == 0 {
			break
		}
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
	return view, nil
}

// ResolveScope looks up a scope node (typically a project) by its identifier
// property, scoped under the given parent.
//
// On miss this is the single most common debugging scenario in Tack. Yesterday's
// incident was an identifier-resolver miss that returned empty silently.
// We log every miss at Info with the inputs that produced it so the next miss
// is diagnosable from one line.
func (r *Resolver) ResolveScope(ctx context.Context, parent *node.NodeView, level ScopeLevel, identifier string) (*node.NodeView, error) {
	// List children of parent with the given type, filter by Props["identifier"].
	idents := strings.ToUpper(identifier)
	rawValue, _ := json.Marshal(idents)

	children, err := r.nodes.ListByProperty(ctx, parent.OrgID, level.TypeKey, "identifier", rawValue)
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", level.Slug, identifier, err)
	}
	// Narrow by parent_id Props to ensure the child is under this parent.
	parentIDRaw, _ := json.Marshal(parent.ID.String())
	for _, n := range children {
		if got, ok := n.Props["parent_id"]; ok && string(got) == string(parentIDRaw) {
			return r.reader.Get(ctx, n.ID)
		}
	}
	// Fallback: return first match with matching identifier even without parent filter.
	if len(children) > 0 {
		return r.reader.Get(ctx, children[0].ID)
	}

	telemetry.IncResolverMiss("scope")
	telemetry.L(ctx).InfoContext(ctx, "resolver.scope.miss",
		slog.String("level", level.TypeKey),
		slog.String("identifier", identifier),
		slog.String("identifier_normalized", idents),
		slog.String("parent_id", parent.ID.String()),
		slog.String("parent_type", parent.NodeType),
		slog.Int("candidate_count", len(children)),
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

// ViewSlug extracts the slug from a node view's Props.
func ViewSlug(v *node.NodeView) string {
	return stringProp(v, "slug")
}

// ViewIdentifier extracts the identifier from a node view's Props.
func ViewIdentifier(v *node.NodeView) string {
	return stringProp(v, "identifier")
}

func stringProp(v *node.NodeView, key string) string {
	if v == nil || v.Props == nil {
		return ""
	}
	raw, ok := v.Props[key]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// ResolveNodeID accepts a UUID, a slug-resolvable identifier, or a sequence-style
// identifier (e.g. "TACK-65"), and returns the node UUID.
func (r *Resolver) ResolveNodeID(ctx context.Context, input string) (uuid.UUID, error) {
	if id, err := uuid.Parse(input); err == nil {
		return id, nil
	}
	projIdent, seqID, err := ParseNodeIdentifier(input)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid node_id %q: must be a UUID or identifier like TACK-65", input)
	}
	userID, ok := auth.UserID(ctx)
	if !ok {
		return uuid.Nil, fmt.Errorf("unauthenticated")
	}
	wss, err := r.WorkspacesForUser(ctx, userID)
	if err != nil {
		return uuid.Nil, err
	}
	for _, ws := range wss {
		if len(r.scopeChain) == 0 {
			continue
		}
		// projIdent (the prefix in "TACK-161") names the topmost scope below the
		// entry point, which is the project level. The deepest chain entry is
		// the parent of the leaf sequence-bearing type and would be wrong here
		// when the chain has more than one level (e.g. [project, issue] for
		// comment).
		first := r.scopeChain[0]
		proj, err := r.ResolveScope(ctx, ws, first, projIdent)
		if err != nil {
			continue
		}
		// For each sequence-bearing type, look for a child of proj with matching sequence.
		for _, typeKey := range r.sequenceTypeKeys {
			rawSeq, _ := json.Marshal(int64(seqID))
			candidates, err := r.nodes.ListByProperty(ctx, ws.OrgID, typeKey, "sequence", rawSeq)
			if err != nil {
				continue
			}
			parentIDRaw, _ := json.Marshal(proj.ID.String())
			for _, n := range candidates {
				if got, ok := n.Props["parent_id"]; ok && string(got) == string(parentIDRaw) {
					return n.ID, nil
				}
			}
		}
	}
	telemetry.IncResolverMiss("node_id")
	telemetry.L(ctx).InfoContext(ctx, "resolver.node_id.miss",
		slog.String("input", input),
		slog.String("project_ident", projIdent),
		slog.Int("sequence_id", seqID),
		slog.Int("workspaces_tried", len(wss)),
		slog.Int("scope_chain_depth", len(r.scopeChain)),
		slog.Int("sequence_types_tried", len(r.sequenceTypeKeys)),
	)
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
