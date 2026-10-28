package tools

import (
	"goodkind.io/tack/internal/domain/node"
)

// listPageReserveBytes is the part of maxSuccessTextBytes kept for the page
// heading, the count line, and the Next cursor line, so the cursor always
// survives capText.
const listPageReserveBytes = 1024

// listPageItemBudget is the byte budget the rendered items of one page share.
const listPageItemBudget = maxSuccessTextBytes - listPageReserveBytes

// Markdown bytes collection.md.tmpl adds around an item's title and around
// each field: "- " plus a newline, and "  - " plus ": " plus a newline.
const (
	itemTitleOverheadBytes = 3
	itemFieldOverheadBytes = 7
)

// renderListPage renders the rows of page that fit the byte budget. When rows
// are left out, or the store reported more, NextCursor resumes after the last
// rendered row, so every row stays reachable through the cursor.
func renderListPage(rc *renderCtx, kind string, page node.Page) string {
	items := make([]markdownItem, 0, len(page.Views))
	usedBytes := 0
	nextCursor := page.NextCursor
	for index, view := range page.Views {
		item := nodeListItem(rc, view)
		if index == 0 {
			// A row larger than the whole budget still renders, shortened, so
			// the page always advances.
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

// markdownItemBytes is the number of bytes collection.md.tmpl writes for item.
func markdownItemBytes(item markdownItem) int {
	size := itemTitleOverheadBytes + len(item.Title)
	for _, field := range item.Fields {
		size += itemFieldOverheadBytes + len(field.Label) + len(field.Value)
	}
	return size
}
