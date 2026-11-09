package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// listPagingCheckLimit sets the requested page size for the paging check.
// The small scale generates more issues per project than this limit. A
// successful check outside a dry run reads at least two pages at that scale.
const listPagingCheckLimit = 5

const (
	nextCursorMarker = "Next cursor: `"
	listItemMarker   = "- `"
)

// verifyListPaging reads the first two pages of tack_list_issues for one
// project. It fails if the first response omits a cursor when the scale
// requires more issues than one page, or if the second page repeats a
// reference from the first.
func (g *Generator) verifyListPaging(ctx context.Context, workspace WorkspaceIdentity, projectReference string) error {
	if g.dryRun {
		return nil
	}
	token := workspace.Actors[0].Token
	arguments := scopeArgs(workspace.Slug, projectReference)
	arguments.Limit = listPagingCheckLimit
	first, err := g.driver.Call(ctx, token, "tack_list_issues", arguments)
	if err != nil {
		return loggedError(ctx, "qa datagen: list issues first page", err)
	}
	cursor := nextCursor(first.Text())
	if cursor == "" {
		if g.scale.IssuesPerProject > listPagingCheckLimit {
			return fmt.Errorf("qa datagen: tack_list_issues printed no cursor for %d issues at limit %d", g.scale.IssuesPerProject, listPagingCheckLimit)
		}
		return nil
	}
	arguments.Cursor = cursor
	second, err := g.driver.Call(ctx, token, "tack_list_issues", arguments)
	if err != nil {
		return loggedError(ctx, "qa datagen: list issues second page", err)
	}
	firstPage := make(map[string]bool)
	for _, reference := range listReferences(first.Text()) {
		firstPage[reference] = true
	}
	for _, reference := range listReferences(second.Text()) {
		if firstPage[reference] {
			return fmt.Errorf("qa datagen: tack_list_issues second page repeats %s", reference)
		}
	}
	slog.InfoContext(ctx, "qa.datagen.list_paging_verified", slog.String("project", projectReference))
	return nil
}

// nextCursor extracts the cursor from a list response. It returns an empty
// string on the last page or when the cursor marker lacks a closing backtick.
func nextCursor(text string) string {
	start := strings.Index(text, nextCursorMarker)
	if start < 0 {
		return ""
	}
	value := text[start+len(nextCursorMarker):]
	end := strings.IndexByte(value, '`')
	if end < 0 {
		return ""
	}
	return value[:end]
}

// listReferences returns the printed reference of every top-level list item.
func listReferences(text string) []string {
	references := make([]string, 0)
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, listItemMarker) {
			continue
		}
		references = append(references, strings.Trim(strings.TrimPrefix(line, "- "), "`"))
	}
	return references
}
