package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

const maxWriteConfirmationBytes = 1024

// TestWriteConfirmationPrintsLongCommentBodyOnce checks that a node whose
// name is its only identity is confirmed by raw id with its name printed
// once, shortened, instead of three full copies.
func TestWriteConfirmationPrintsLongCommentBodyOnce(t *testing.T) {
	body := "unique comment body " + strings.Repeat("y", 5*1024)
	view := &node.NodeView{ID: uuid.Must(uuid.NewV7()), NodeType: "comment", Name: body}
	rc := newRenderCtxWithTypes(context.Background(), nil, nil, nil)

	text := renderWriteConfirmation(rc, "Created", view, []string{"name"})

	if len(text) > maxWriteConfirmationBytes {
		t.Fatalf("confirmation is %d bytes, want at most %d:\n%s", len(text), maxWriteConfirmationBytes, text)
	}
	if strings.Count(text, "unique comment body") != 1 {
		t.Fatalf("comment body printed %d times:\n%s", strings.Count(text, "unique comment body"), text)
	}
	if !strings.Contains(text, "- Raw id: `"+view.ID.String()+"`") {
		t.Fatalf("confirmation lacks the raw id:\n%s", text)
	}
}
