package integration

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	readerPageBytes     = 128
	readerExcludedValue = "EXCLUDED-SECRET-VALUE"
)

func readerIncludedValue() string {
	return strings.Repeat("界🙂é́ ", 60) + "[link](https://example.test/" + strings.Repeat("x", 200) + ") final-target"
}

func TestSearchReaderRejectsMissingNode(t *testing.T) {
	stores := newSearchStore(t)
	reader := stores.SearchContent(stores.SearchPolicySet())
	_, err := reader.Content(t.Context(), searchdomain.ContentRequest{
		NodeID: uuid.Must(uuid.NewV7()), Cursor: "", ProjectionConfig: "", AccessVersions: nil, MaxBytes: readerPageBytes, SearchGeneration: 0,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("content error = %v, want not found", err)
	}
}

func TestSearchReaderPagesDeclaredUnicodeText(t *testing.T) {
	stores := newSearchStore(t)
	included := readerIncludedValue()
	fixture := putSearchText(t, stores, included, readerExcludedValue)
	pages := readSearchPages(t, stores, fixture.NodeID, readerPageBytes)
	if len(pages) < 4 {
		t.Fatalf("reader returned %d pages, want a multi-page node", len(pages))
	}
	for number, page := range pages {
		if len(page.Text) > readerPageBytes || !utf8.ValidString(page.Text) || page.Ordinal != uint64(number) {
			t.Fatalf("page %d violates the UTF-8 byte bound or ordinal order", number)
		}
		if page.OverlapBytes > readerPageBytes/4 || page.OverlapBytes >= len(page.Text) {
			t.Fatalf("page %d overlap %d exceeds the reserved quarter", number, page.OverlapBytes)
		}
		if number > 0 && page.OverlapBytes == 0 {
			t.Fatalf("continuation page %d repeats no overlap", number)
		}
		if !page.Done && (page.NextCursor == "" || number > 0 && page.NextCursor == pages[number-1].NextCursor) {
			t.Fatalf("page %d did not advance its cursor", number)
		}
		if strings.Contains(page.Text, readerExcludedValue) {
			t.Fatalf("page %d contains the excluded value", number)
		}
	}
	if got, want := uniqueSearchText(pages), searchFixtureName+"\n"+included+"\n"; got != want {
		t.Fatalf("unique projected text differs: got %d bytes, want %d", len(got), len(want))
	}
	renamed := putSearchTextInOrg(t, stores, fixture.OrgID, included, readerExcludedValue)
	if uniqueSearchText(readSearchPages(t, stores, renamed.NodeID, readerPageBytes)) != uniqueSearchText(pages) {
		t.Fatal("renaming the opaque type and property keys changed the projected text")
	}
}

// TestSearchReaderCompletesOnAFullFinalPage requires the reader to report
// completion on a final page that fills the byte bound exactly, with no
// continuation cursor. The projected text is the fixture name line and the
// included value line. Each continuation repeats a quarter of the bound, so
// a text of one bound plus two advances ends on the third full page.
func TestSearchReaderCompletesOnAFullFinalPage(t *testing.T) {
	stores := newSearchStore(t)
	advance := readerPageBytes - readerPageBytes/4
	projectedLength := readerPageBytes + 2*advance
	nameLine := len(searchFixtureName) + len("\n")
	included := strings.Repeat("b", projectedLength-nameLine-len("\n"))
	fixture := putSearchText(t, stores, included, readerExcludedValue)
	reader := stores.SearchContent(stores.SearchPolicySet())
	request := searchdomain.ContentRequest{
		NodeID: fixture.NodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil, MaxBytes: readerPageBytes, SearchGeneration: 0,
	}
	const finalOrdinal = 2
	for ordinal := range finalOrdinal + 1 {
		page, err := reader.Content(t.Context(), request)
		if err != nil {
			t.Fatalf("read page %d: %v", ordinal, err)
		}
		if len(page.Text) != readerPageBytes {
			t.Fatalf("page %d has %d bytes, want the full bound of %d", ordinal, len(page.Text), readerPageBytes)
		}
		final := ordinal == finalOrdinal
		if page.Done != final || (page.NextCursor == "") != final {
			t.Fatalf("page %d reports done %t with cursor %q, want completion only on the full final page %d", ordinal, page.Done, page.NextCursor, finalOrdinal)
		}
		request.Cursor = page.NextCursor
	}
}

func TestSearchReaderRejectsEditBetweenPages(t *testing.T) {
	stores := newSearchStore(t)
	fixture := putSearchText(t, stores, readerIncludedValue(), readerExcludedValue)
	reader := stores.SearchContent(stores.SearchPolicySet())
	request := searchdomain.ContentRequest{
		NodeID: fixture.NodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil, MaxBytes: readerPageBytes, SearchGeneration: 0,
	}
	first, err := reader.Content(t.Context(), request)
	if err != nil {
		t.Fatalf("read first page: %v", err)
	}
	writeSearchNode(t, stores, fixture, readerIncludedValue()+" edited", readerExcludedValue)
	request.Cursor = first.NextCursor
	if _, err := reader.Content(t.Context(), request); !errors.Is(err, node.ErrContentChanged) {
		t.Fatalf("continuation after an edit returned %v, want content changed", err)
	}
	restarted, err := reader.Content(t.Context(), searchdomain.ContentRequest{
		NodeID: fixture.NodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil, MaxBytes: readerPageBytes, SearchGeneration: 0,
	})
	if err != nil || restarted.Revision == first.Revision {
		t.Fatalf("first page after the edit = revision %q error %v, want a new revision", restarted.Revision, err)
	}
}

func TestSearchReaderRejectsDeletedNode(t *testing.T) {
	stores := newSearchStore(t)
	fixture := putSearchText(t, stores, readerIncludedValue(), readerExcludedValue)
	reader := stores.SearchContent(stores.SearchPolicySet())
	first, err := reader.Content(t.Context(), searchdomain.ContentRequest{
		NodeID: fixture.NodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil, MaxBytes: readerPageBytes, SearchGeneration: 0,
	})
	if err != nil {
		t.Fatalf("read first page: %v", err)
	}
	if err := stores.Nodes.Delete(t.Context(), fixture.OrgID, fixture.NodeID); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	_, err = reader.Content(t.Context(), searchdomain.ContentRequest{
		NodeID: fixture.NodeID, Cursor: first.NextCursor, ProjectionConfig: "", AccessVersions: nil, MaxBytes: readerPageBytes, SearchGeneration: 0,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("continuation after deletion returned %v, want not found", err)
	}
}
