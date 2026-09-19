package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

var renderedCursorPattern = regexp.MustCompile("Next cursor: `([^`]+)`")

func longNameViews(count int, nameBytes int) []*node.NodeView {
	views := make([]*node.NodeView, 0, count)
	for i := 0; i < count; i++ {
		prefix := fmt.Sprintf("long comment %03d ", i)
		views = append(views, &node.NodeView{
			ID:       uuid.Must(uuid.NewV7()),
			NodeType: "comment",
			Name:     prefix + strings.Repeat("x", nameBytes-len(prefix)),
		})
	}
	return views
}

// TestRenderListPageKeepsCursorWhenRowsExceedBudget renders a full page whose
// rows total far more than the response cap and checks that the page stops
// early, stays under the cap, and resumes after the last row it printed.
func TestRenderListPageKeepsCursorWhenRowsExceedBudget(t *testing.T) {
	views := longNameViews(100, 2048)
	rc := newRenderCtxWithTypes(context.Background(), nil, nil, nil)

	text := renderListPage(rc, "comments", node.Page{Views: views, NextCursor: ""})

	if len(text) > maxSuccessTextBytes {
		t.Fatalf("page is %d bytes, want at most %d", len(text), maxSuccessTextBytes)
	}
	match := renderedCursorPattern.FindStringSubmatch(text)
	if match == nil {
		t.Fatalf("page has no Next cursor line; tail: %q", text[len(text)-200:])
	}
	lastID, err := node.DecodeCursor(match[1])
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	rendered := strings.Count(text, "\n- `long comment ")
	if rendered == 0 || rendered == len(views) {
		t.Fatalf("rendered %d of %d rows, want a strict subset", rendered, len(views))
	}
	if lastID != views[rendered-1].ID {
		t.Fatalf("cursor resumes after %s, want the last rendered row %s", lastID, views[rendered-1].ID)
	}
	if strings.Contains(text, fmt.Sprintf("long comment %03d ", rendered)) {
		t.Fatalf("row %d is past the cursor but was rendered", rendered)
	}
	if !strings.Contains(text, fmt.Sprintf("%d comments shown.", rendered)) {
		t.Fatalf("count line does not match %d rendered rows", rendered)
	}
}

// TestRenderListPageShortensSingleOversizedRow checks that one row larger
// than the whole budget still renders, shortened, with its cursor intact.
func TestRenderListPageShortensSingleOversizedRow(t *testing.T) {
	views := longNameViews(2, 60*1024)
	rc := newRenderCtxWithTypes(context.Background(), nil, nil, nil)

	text := renderListPage(rc, "comments", node.Page{Views: views, NextCursor: ""})

	if len(text) > maxSuccessTextBytes {
		t.Fatalf("page is %d bytes, want at most %d", len(text), maxSuccessTextBytes)
	}
	match := renderedCursorPattern.FindStringSubmatch(text)
	if match == nil {
		t.Fatal("page has no Next cursor line")
	}
	lastID, err := node.DecodeCursor(match[1])
	if err != nil || lastID != views[0].ID {
		t.Fatalf("cursor = %s, %v; want the first row %s", lastID, err, views[0].ID)
	}
	if !strings.Contains(text, "long comment 000 ") || !strings.Contains(text, truncatedTitleSuffix) {
		t.Fatal("first row is missing or was not shortened")
	}
}
