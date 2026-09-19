package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// listPagingCheckLimit is the page size the paging check requests. The small
// scale generates more issues than this per project, so the check always
// reads at least two pages there.
const listPagingCheckLimit = 5

// searchCheckAttempts and searchCheckInterval bound how long the search check
// waits for Meilisearch to index a generated issue.
const (
	searchCheckAttempts = 20
	searchCheckInterval = 500 * time.Millisecond
)

const (
	nextCursorMarker = "Next cursor: `"
	listItemMarker   = "- `"
)

// verifyListPaging reads the first two pages of tack_list_issues for one
// project and fails when a generated project with more issues than one page
// prints no cursor, or when the second page repeats a reference from the
// first.
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

// verifySearchFindsIssue calls tack_search for titleWord until the result
// lists the issue named issueName. The check matches the name because
// ensureNode returns the raw id, which search results never print.
// Meilisearch indexes asynchronously, so the check retries before it reports
// the issue as unsearchable.
func (g *Generator) verifySearchFindsIssue(ctx context.Context, token string, workspace WorkspaceIdentity, issueName, titleWord string) error {
	if g.dryRun || issueName == "" || titleWord == "" {
		return nil
	}
	arguments := ToolArguments{WorkspaceReference: workspace.Slug, Query: titleWord}
	reference := ""
	for attempt := 0; attempt < searchCheckAttempts && reference == ""; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return loggedError(ctx, "qa datagen: search check canceled", ctx.Err())
			case <-time.After(searchCheckInterval):
			}
		}
		result, err := g.driver.Call(ctx, token, "tack_search", arguments)
		if err != nil {
			return loggedError(ctx, "qa datagen: search for "+titleWord, err)
		}
		reference = result.ReferenceForName(issueName)
	}
	if reference == "" {
		return fmt.Errorf("qa datagen: tack_search for %q never returned issue %q after %d attempts", titleWord, issueName, searchCheckAttempts)
	}
	slog.InfoContext(ctx, "qa.datagen.search_verified",
		slog.String("reference", reference), slog.String("query", titleWord))
	return nil
}

// firstWord returns the first whitespace-separated word of name, or "".
func firstWord(name string) string {
	words := strings.Fields(name)
	if len(words) == 0 {
		return ""
	}
	return words[0]
}

// nextCursor returns the cursor a list response prints, or "" on the last page.
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
