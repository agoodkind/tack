package tools

import (
	"context"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/user"
)

// renderCtx stores the dependencies for resolving references in one Markdown
// response.
type renderCtx struct {
	ctx       context.Context
	reader    node.NodeReader
	users     user.Repository
	typeIndex map[string]*node.NodeType
	cache     map[uuid.UUID]string
	// assignees maps node IDs to user IDs from assigned_to relationships.
	// loadAssignees fills the map.
	assignees map[uuid.UUID][]uuid.UUID
}

func newRenderCtxWithTypes(ctx context.Context, reader node.NodeReader, users user.Repository, typeIndex map[string]*node.NodeType) *renderCtx {
	return &renderCtx{ctx: ctx, reader: reader, users: users, typeIndex: typeIndex, cache: map[uuid.UUID]string{}, assignees: map[uuid.UUID][]uuid.UUID{}}
}

func (rc *renderCtx) nodeIdentifier(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	if cached, ok := rc.cache[id]; ok {
		return cached
	}
	view, err := rc.reader.Get(rc.ctx, id)
	if err != nil || view == nil {
		rc.cache[id] = ""
		return ""
	}
	out := identifierFor(view, rc)
	rc.cache[id] = out
	return out
}

func (rc *renderCtx) nodeSummary(id uuid.UUID) (string, string, string) {
	if id == uuid.Nil || rc == nil || rc.reader == nil {
		return "", "", ""
	}
	view, err := rc.reader.Get(rc.ctx, id)
	if err != nil || view == nil {
		return "", "", ""
	}
	ident := identifierFor(view, rc)
	rc.cache[id] = ident
	return ident, view.Name, view.NodeType
}

// nodeSummaryInOrg returns a node's reference and name when its organization
// matches orgID or the request membership set includes that organization.
// The relationship list and change renderers print the bare UUID for an
// endpoint outside the given organization and the caller's memberships. In
// those outputs, a single-organization caller cannot read the foreign node's
// name through a relationship that a member of both organizations created.
func (rc *renderCtx) nodeSummaryInOrg(id uuid.UUID, orgID uuid.UUID) (string, string) {
	if id == uuid.Nil || rc == nil || rc.reader == nil {
		return "", ""
	}
	view, err := rc.reader.Get(rc.ctx, id)
	if err != nil || view == nil {
		return "", ""
	}
	if view.OrgID != orgID && !callerMayViewOrg(rc.ctx, view.OrgID) {
		return "", ""
	}
	ident := identifierFor(view, rc)
	rc.cache[id] = ident
	return ident, view.Name
}

// callerMayViewOrg reports whether the request membership set includes
// orgID.
// It returns false when the request does not contain a membership set.
func callerMayViewOrg(ctx context.Context, orgID uuid.UUID) bool {
	orgIDs, ok := auth.OrgMembership(ctx)
	if !ok {
		return false
	}
	return slices.Contains(orgIDs, orgID)
}

func identifierFor(view *node.NodeView, rc *renderCtx) string {
	if view == nil {
		return ""
	}
	if rc != nil {
		if ident := rc.referenceIdentifierFor(view); ident != "" {
			return ident
		}
	}
	if ident := stringProp(view, "identifier"); ident != "" {
		return ident
	}
	if slug := stringProp(view, "slug"); slug != "" {
		return slug
	}
	return view.Name
}

func (rc *renderCtx) userDisplay(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	if cached, ok := rc.cache[id]; ok {
		return cached
	}
	if rc.users == nil {
		rc.cache[id] = ""
		return ""
	}
	u, err := rc.users.GetByID(rc.ctx, id)
	if err != nil || u == nil {
		rc.cache[id] = ""
		return ""
	}
	out := u.DisplayName
	if out == "" {
		out = u.Email
	}
	rc.cache[id] = out
	return out
}
