package datagen

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// maxSearchTraversalPages bounds one continuation traversal.
const maxSearchTraversalPages = 1000

// searchPage is one parsed public tack_search response.
type searchPage struct {
	IDs           []uuid.UUID
	Cursor        string
	ResponseBytes int
}

// searchLimits are the public per-response bounds a traversal enforces.
type searchLimits struct {
	MaxResults, MaxResponseBytes int
}

// callSearch calls tack_search with token under entryReference. It uses
// identities to resolve result node IDs and reads the continuation cursor.
func callSearch(ctx context.Context, driver *Driver, identities SearchIdentities, token, entryReference, query, cursor string) (searchPage, error) {
	return callTypedSearch(ctx, driver, identities, token, entryReference, query, "", cursor)
}

// callTypedSearch calls tack_search with a nodeType filter and resolves each
// result through identities. An empty nodeType searches all types.
func callTypedSearch(ctx context.Context, driver *Driver, identities SearchIdentities, token, entryReference, query, nodeType, cursor string) (searchPage, error) {
	text, err := searchPageText(ctx, driver, token, entryReference, query, nodeType, cursor)
	if err != nil {
		return searchPage{}, err
	}
	nodeIDs, err := identities.IDs(ctx, SearchResultItems(text))
	if err != nil {
		return searchPage{}, loggedError(ctx, fmt.Sprintf("qa datagen: resolve search %q results", query), err)
	}
	return searchPage{IDs: nodeIDs, Cursor: nextCursor(text), ResponseBytes: len(text)}, nil
}

// searchPageText calls tack_search once and returns the page text.
func searchPageText(ctx context.Context, driver *Driver, token, entryReference, query, nodeType, cursor string) (string, error) {
	arguments := ToolArguments{
		WorkspaceReference: entryReference, ProjectReference: "", IssueReference: "", Name: "",
		Properties: nil, NodeID: "", Query: query, NodeType: nodeType, Direction: "", SourceID: "",
		RelationType: "", TargetID: "", Cursor: cursor,
	}
	result, err := driver.Call(ctx, token, "tack_search", arguments)
	if err != nil {
		return "", loggedError(ctx, fmt.Sprintf("qa datagen: search %q of type %q", query, nodeType), err)
	}
	return result.Text(), nil
}

// traverseSearch follows query's continuation cursors and resolves result IDs
// through identities. It rejects duplicate nodes, repeated cursors, and pages
// above the public result or byte limit. It returns node IDs in response order.
func traverseSearch(ctx context.Context, driver *Driver, identities SearchIdentities, token, entryReference, query string, limits searchLimits) ([]uuid.UUID, error) {
	return traversePages(query, limits, func(cursor string) (searchPage, error) {
		return callSearch(ctx, driver, identities, token, entryReference, query, cursor)
	})
}

// traversePages is traverseSearch with request as the page request for each cursor.
func traversePages(query string, limits searchLimits, request func(cursor string) (searchPage, error)) ([]uuid.UUID, error) {
	seen := make(map[uuid.UUID]struct{})
	cursors := make(map[string]struct{})
	ordered := make([]uuid.UUID, 0)
	cursor := ""
	for range maxSearchTraversalPages {
		page, err := request(cursor)
		if err != nil {
			return nil, err
		}
		if len(page.IDs) > limits.MaxResults || page.ResponseBytes > limits.MaxResponseBytes {
			return nil, fmt.Errorf("qa datagen: search %q returned %d nodes in %d bytes, above %d nodes or %d bytes",
				query, len(page.IDs), page.ResponseBytes, limits.MaxResults, limits.MaxResponseBytes)
		}
		for _, nodeID := range page.IDs {
			if _, duplicate := seen[nodeID]; duplicate {
				return nil, fmt.Errorf("qa datagen: search %q returned node %s twice", query, nodeID)
			}
			seen[nodeID] = struct{}{}
			ordered = append(ordered, nodeID)
		}
		if page.Cursor == "" {
			return ordered, nil
		}
		if _, repeated := cursors[page.Cursor]; repeated {
			return nil, fmt.Errorf("qa datagen: search %q repeated a continuation cursor", query)
		}
		cursors[page.Cursor] = struct{}{}
		cursor = page.Cursor
	}
	return nil, fmt.Errorf("qa datagen: search %q did not finish within %d pages", query, maxSearchTraversalPages)
}

// errMissing returns err, or an error for a record that does not exist.
func errMissing(err error) error {
	if err != nil {
		return err
	}
	return errors.New("record does not exist")
}
