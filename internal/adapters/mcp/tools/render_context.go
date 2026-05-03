package tools

import (
	"context"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/user"
)

// renderCtx carries per-call dependencies for resolving references in Markdown output.
type renderCtx struct {
	ctx       context.Context
	reader    node.NodeReader
	users     user.Repository
	typeIndex map[string]*node.NodeType
	cache     map[uuid.UUID]string
}

func newRenderCtxWithTypes(ctx context.Context, reader node.NodeReader, users user.Repository, typeIndex map[string]*node.NodeType) *renderCtx {
	return &renderCtx{ctx: ctx, reader: reader, users: users, typeIndex: typeIndex, cache: map[uuid.UUID]string{}}
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
