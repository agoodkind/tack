package tools

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/service"
)

// searchCursorParam is the optional continuation argument.
const searchCursorParam = "cursor"

// runSearch resolves the workspace entry point, reads the caller's
// organization memberships, runs one ranked page, and renders the bounded
// result list.
func runSearch(ctx context.Context, request mcpmcp.CallToolRequest, nodes NodeTypeBinding, binding SearchBinding) *mcpmcp.CallToolResult {
	resolver := nodes.Resolver
	args, err := bindArgs(request)
	if err != nil {
		return recoverableError("arguments must be a JSON object: " + err.Error())
	}
	entryParam := resolver.EntryPointParamName()
	reference, ok := requireString(args, entryParam)
	if !ok {
		return recoverableError(entryParam + " is required")
	}
	text, ok := requireString(args, "query")
	if !ok {
		return recoverableError("query is required and must be a nonempty string")
	}
	nodeType, ok := resolveSearchNodeType(resolver, optionalString(args, "node_type"))
	if !ok {
		return recoverableError("node_type does not name a node type in this workspace")
	}
	entry, err := resolver.Workspace(ctx, reference)
	if err != nil {
		return classifyError(ctx, err)
	}
	principal, err := mustUser(ctx)
	if err != nil {
		return recoverableError(err.Error())
	}
	members, err := resolver.callerOrgIDs(ctx)
	if err != nil {
		return unexpectedError(ctx, err)
	}
	searchRequest := service.SearchRequest{
		PrincipalID: principal, EntryPointID: entry.ID, MemberOrganizations: members,
		Text: text, NodeType: nodeType, SessionID: uuid.Nil, Version: 0,
	}
	if cursor := optionalString(args, searchCursorParam); cursor != "" {
		searchRequest.SessionID, searchRequest.Version, err = binding.Cursors.Decode(cursor)
		if err != nil {
			return recoverableError("cursor is invalid; start a new search without cursor")
		}
	}
	page, err := binding.Runner.Search(ctx, searchRequest)
	if err != nil {
		return searchFailureResult(ctx, err)
	}
	views, err := searchResultViews(ctx, nodes.Reader, page.Results)
	if err != nil {
		return unexpectedError(ctx, err)
	}
	rc := newRenderCtxWithTypes(ctx, nodes.Reader, nodes.Users, resolver.typeIndex)
	if err := rc.loadAssignees(nodes.Relationships, slices.Collect(maps.Values(views))...); err != nil {
		return classifyError(ctx, err)
	}
	return successText(renderSearchPage(rc, page, views, binding.Cursors), "")
}

// resolveSearchNodeType accepts a node type key or slug and returns its key.
func resolveSearchNodeType(resolver *Resolver, input string) (string, bool) {
	if input == "" {
		return "", true
	}
	for key, nodeType := range resolver.typeIndex {
		if key == input || strings.EqualFold(nodeType.Slug, input) {
			return nodeType.TypeKey, true
		}
	}
	return "", false
}

// searchFailureResult returns an explicit error for every failed call.
func searchFailureResult(ctx context.Context, err error) *mcpmcp.CallToolResult {
	switch {
	case errors.Is(err, searchdomain.ErrInvalidQuery):
		return recoverableError("query is invalid: " + err.Error())
	case errors.Is(err, searchaccess.ErrAccessDenied):
		noteOutcome(ctx, audit.OutcomeRefused, accessDeniedCode, "the caller cannot search this entry point")
		return recoverableError("the caller cannot search this entry point")
	case errors.Is(err, searchdomain.ErrSessionExpired), errors.Is(err, searchdomain.ErrSessionNotFound):
		return recoverableError("the search cursor expired; start a new search without cursor")
	case errors.Is(err, searchdomain.ErrSessionMismatch), errors.Is(err, searchdomain.ErrSessionChanged):
		return recoverableError("the search cursor does not match this request or is no longer current; start a new search without cursor")
	case errors.Is(err, searchdomain.ErrSnapshotLost):
		return recoverableError("the search snapshot ended; start a new search without cursor")
	case errors.Is(err, searchdomain.ErrNoServingIndex):
		return recoverableError("no search index is provisioned")
	case errors.Is(err, searchdomain.ErrEngineUnavailable):
		return searchUnavailable(ctx)
	default:
		return unexpectedError(ctx, err)
	}
}
