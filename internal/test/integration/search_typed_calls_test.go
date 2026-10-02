package integration

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
)

func tryTypedSearch(harness *MCPHarness, query, nodeType, cursor string) (searchPage, error) {
	arguments := searchArguments(harness, query, cursor)
	arguments["node_type"] = nodeType
	text, isError, err := rawSearchCall(harness, arguments)
	if err != nil {
		return searchPage{}, err
	}
	if isError {
		return searchPage{}, errors.New(text)
	}
	return parseSearchPage(text)
}

func callTypedSearch(t *testing.T, harness *MCPHarness, query, nodeType, cursor string) searchPage {
	t.Helper()
	page, err := tryTypedSearch(harness, query, nodeType, cursor)
	if err != nil {
		t.Fatalf("typed search %q: %v", query, err)
	}
	return page
}

func everyTypedSearchPage(t *testing.T, fixture queryFixture, query, nodeType string) []uuid.UUID {
	t.Helper()
	var ids []uuid.UUID
	var tokens []byte
	cursor := ""
	for number := range 10_000 {
		page := callTypedSearch(t, fixture.Harness, query, nodeType, cursor)
		if number == 0 && page.Complete {
			t.Fatal("the typed continuation corpus returned no cursor")
		}
		if cursor != "" {
			continued := cursorSession(t, fixture, cursor)
			if !bytes.Equal(tokens, continued.Snapshot.QueryTokens) {
				t.Fatal("typed continuation changed its saved token map")
			}
			replay := callTypedSearch(t, fixture.Harness, query, nodeType, cursor)
			if !slices.Equal(page.IDs, replay.IDs) || page.Cursor != replay.Cursor || page.Complete != replay.Complete {
				t.Fatal("typed continuation replay changed its result")
			}
		}
		ids = append(ids, page.IDs...)
		if page.Complete {
			return ids
		}
		cursor = page.Cursor
		if tokens == nil {
			tokens = bytes.Clone(cursorSession(t, fixture, cursor).Snapshot.QueryTokens)
		}
	}
	t.Fatal("typed search did not complete within 10000 pages")
	return nil
}
