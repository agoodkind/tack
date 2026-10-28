package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// searchResultViews reads each ranked result with NodeReader.Get.
func searchResultViews(ctx context.Context, reader node.NodeReader, results []node.Summary) (map[uuid.UUID]*node.NodeView, error) {
	views := make(map[uuid.UUID]*node.NodeView, len(results))
	for _, result := range results {
		view, err := reader.Get(ctx, result.ID)
		if err != nil {
			slog.ErrorContext(ctx, "search.result_read_failed", slog.String("node_id", result.ID.String()), slog.String("err", err.Error()))
			return nil, fmt.Errorf("read search result node %s: %w", result.ID, err)
		}
		if view != nil {
			views[result.ID] = view
		}
	}
	return views, nil
}

// renderSearchPage renders results in rank order with the shared list layout
// and a Raw id field, within each result's service.ResultBytes budget.
func renderSearchPage(rc *renderCtx, page service.SearchPage, views map[uuid.UUID]*node.NodeView, cursors *SearchCursorCodec) string {
	items := make([]markdownItem, 0, len(page.Results))
	for _, result := range page.Results {
		view := views[result.ID]
		if view == nil {
			continue
		}
		rawID := markdownCodeFieldValue("Raw id", view.ID.String())
		rawIDBytes := itemFieldOverheadBytes + len(rawID.Label) + len(rawID.Value)
		item := fitItemToBudget(nodeListItem(rc, view), service.ResultBytes(result)-rawIDBytes)
		item.Fields = append(item.Fields, rawID)
		items = append(items, item)
		addNodeTargets(rc.ctx, view)
	}
	nextCursor := ""
	if !page.Complete {
		nextCursor = cursors.Encode(page.SessionID, page.NextVersion)
	}
	data := collectionTemplateData{Heading: "Search results", Count: len(items), Noun: "results", Items: items, NextCursor: nextCursor}
	return executeMarkdownTemplate("collection.md.tmpl", data)
}
