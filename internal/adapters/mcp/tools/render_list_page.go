package tools

import (
	"goodkind.io/tack/internal/domain/node"
)

// listPageReserveBytes reserves part of maxSuccessTextBytes for the page
// heading, count line, and Next cursor line before capText limits the
// response.
const listPageReserveBytes = 1024

// listPageItemBudget is the byte budget that the rendered items on one page
// share.
const listPageItemBudget = maxSuccessTextBytes - listPageReserveBytes

// The collection template adds "- " and a newline around each item title. It
// adds "  - ", ": ", and a newline around each field.
const (
	itemTitleOverheadBytes = 3
	itemFieldOverheadBytes = 7
)

// renderListPage attempts to shorten the first row to the item budget, then
// adds rows while they fit. If it omits rows, NextCursor identifies the last
// rendered row for the next request. Otherwise, it preserves the store's
// cursor for any remaining rows.
func renderListPage(rc *renderCtx, kind string, page node.Page) string {
	items := make([]markdownItem, 0, len(page.Views))
	usedBytes := 0
	nextCursor := page.NextCursor
	for index, view := range page.Views {
		item := nodeListItem(rc, view)
		if index == 0 {
			// Render the first row even when shortening cannot reduce it to the whole
			// budget. The next page can then start after that row.
			item = fitItemToBudget(item, listPageItemBudget)
		}
		size := markdownItemBytes(item)
		if index > 0 && usedBytes+size > listPageItemBudget {
			nextCursor = node.EncodeCursor(page.Views[index-1].ID)
			break
		}
		usedBytes += size
		items = append(items, item)
		addNodeTargets(rc.ctx, view)
	}
	data := collectionTemplateData{Heading: titleText(kind), Count: len(items), Noun: kind, Items: items, NextCursor: nextCursor}
	return executeMarkdownTemplate("collection.md.tmpl", data)
}

// markdownItemBytes counts the bytes that the collection template writes for
// item.
func markdownItemBytes(item markdownItem) int {
	size := itemTitleOverheadBytes + len(item.Title)
	for _, field := range item.Fields {
		size += itemFieldOverheadBytes + len(field.Label) + len(field.Value)
	}
	return size
}
