package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/telemetry"
)

// searchPage is one parsed public tack_search response.
type searchPage struct {
	IDs      []uuid.UUID
	Cursor   string
	Complete bool
}

// searchPages is every parsed page of one traversal.
type searchPages struct {
	IDs     []uuid.UUID
	Cursors []string
}

func searchArguments(harness *MCPHarness, query, cursor string) map[string]any {
	arguments := map[string]any{"workspace_reference": harness.Workspace, "query": query}
	if cursor != "" {
		arguments["cursor"] = cursor
	}
	return arguments
}

// callSearch calls tack_search through the production HTTP handler and
// fails the test on an error result.
func callSearch(t *testing.T, harness *MCPHarness, query, cursor string) searchPage {
	t.Helper()
	page, err := trySearch(harness, query, cursor)
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	return page
}

// trySearch performs one authenticated call and returns an error instead of
// failing the test. Concurrent goroutines use it.
func trySearch(harness *MCPHarness, query, cursor string) (searchPage, error) {
	text, isError, err := rawSearchCall(harness, searchArguments(harness, query, cursor))
	if err != nil {
		return searchPage{}, err
	}
	if isError {
		return searchPage{}, errors.New(text)
	}
	return parseSearchPage(text, harness.titles)
}

func rawSearchCall(harness *MCPHarness, arguments map[string]any) (string, bool, error) {
	requestID := uuid.NewString()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": requestID, "method": "tools/call",
		"params": map[string]any{"name": "tack_search", "arguments": arguments},
	})
	if err != nil {
		return "", false, fmt.Errorf("encode search request: %w", err)
	}
	requestContext := telemetry.WithRequestMetadata(context.Background(), requestID)
	request := httptest.NewRequestWithContext(requestContext, http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set(telemetry.RequestIDHeader, requestID)
	request.Header.Set("Authorization", "Bearer "+harness.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	recorder := httptest.NewRecorder()
	harness.handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("search HTTP status %d: %s", response.StatusCode, payload)
	}
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		for _, line := range bytes.Split(payload, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data: ")) {
				payload = bytes.TrimPrefix(line, []byte("data: "))
			}
		}
	}
	var decoded struct {
		Result rawToolResult `json:"result"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return "", false, fmt.Errorf("decode search response: %w", err)
	}
	if !decoded.Result.IsError {
		if err := requireSearchAuditInvocation(requestContext, harness, requestID); err != nil {
			return "", false, err
		}
	}
	return toolResultText(decoded.Result), decoded.Result.IsError, nil
}

func rawCallStatus(t *testing.T, harness *MCPHarness, tool string, arguments map[string]any) int {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": uuid.NewString(), "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": arguments},
	})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	if harness.token != "" {
		request.Header.Set("Authorization", "Bearer "+harness.token)
	}
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	harness.handler.ServeHTTP(recorder, request)
	return recorder.Code
}

// searchCursorMarker precedes the continuation cursor on a page that has
// more results.
const searchCursorMarker = "Next cursor: `"

// parseSearchPage checks the result count, resolves result identities with
// titles, and reads the continuation cursor. It marks a page complete when
// the response omits the cursor.
func parseSearchPage(text string, titles *datagen.SearchTitles) (searchPage, error) {
	items := datagen.SearchResultItems(text)
	nodeIDs, err := titles.IDs(items)
	if err != nil {
		return searchPage{}, fmt.Errorf("%w:\n%s", err, text)
	}
	page := searchPage{IDs: nodeIDs, Cursor: "", Complete: false}
	for _, line := range strings.Split(text, "\n") {
		if marker := strings.Index(line, searchCursorMarker); marker >= 0 {
			page.Cursor = strings.TrimSuffix(line[marker+len(searchCursorMarker):], "`")
		}
	}
	page.Complete = page.Cursor == ""
	countLine := fmt.Sprintf("\n%d results shown.\n", len(items))
	if !strings.Contains(text, countLine) {
		return searchPage{}, fmt.Errorf("page has %d result items but no matching count line:\n%s", len(items), text)
	}
	return page, nil
}

// searchResultItems groups each result title with its following field lines.
func searchResultItems(text string) []string {
	var items []string
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "- "):
			items = append(items, line)
		case strings.HasPrefix(line, "  - ") && len(items) > 0:
			items[len(items)-1] += "\n" + line
		}
	}
	return items
}

// callEverySearchPage follows each cursor until a page omits the cursor.
// It alternates between harnesses when the caller supplies more than one.
func callEverySearchPage(t *testing.T, query string, harnesses ...*MCPHarness) searchPages {
	t.Helper()
	pages := searchPages{IDs: []uuid.UUID{}, Cursors: []string{}}
	cursor := ""
	for number := range 10_000 {
		page := callSearch(t, harnesses[number%len(harnesses)], query, cursor)
		pages.IDs = append(pages.IDs, page.IDs...)
		if page.Complete {
			return pages
		}
		pages.Cursors = append(pages.Cursors, page.Cursor)
		cursor = page.Cursor
	}
	t.Fatalf("search %q did not complete within 10000 pages", query)
	return pages
}

// requireExactlyOnce requires every expected ID once and no other ID.
func requireExactlyOnce(t *testing.T, got, expected []uuid.UUID) {
	t.Helper()
	seen := make(map[uuid.UUID]int, len(got))
	for _, id := range got {
		seen[id]++
	}
	for _, id := range expected {
		if seen[id] != 1 {
			t.Fatalf("node %s returned %d times, want once", id, seen[id])
		}
	}
	if len(got) != len(expected) {
		t.Fatalf("search returned %d results, want %d", len(got), len(expected))
	}
}

// requireCorpusOnce requires every expected ID once and no other ID except
// the caller's entry-point node. It accepts the entry-point node at most once
// because semantic ranking can return that searchable node for any query.
func requireCorpusOnce(t *testing.T, got, expected []uuid.UUID, entryID uuid.UUID) {
	t.Helper()
	corpus := make([]uuid.UUID, 0, len(got))
	for _, id := range got {
		if id != entryID {
			corpus = append(corpus, id)
		}
	}
	if entryCount := len(got) - len(corpus); entryCount > 1 {
		t.Fatalf("entry-point node %s returned %d times, want at most once", entryID, entryCount)
	}
	requireExactlyOnce(t, corpus, expected)
}
