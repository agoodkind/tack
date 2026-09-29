package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
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
	return parseSearchPage(text)
}

func rawSearchCall(harness *MCPHarness, arguments map[string]any) (string, bool, error) {
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": uuid.NewString(), "method": "tools/call",
		"params": map[string]any{"name": "tack_search", "arguments": arguments},
	})
	if err != nil {
		return "", false, fmt.Errorf("encode search request: %w", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
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
	return toolResultText(decoded.Result), decoded.Result.IsError, nil
}

// rawCallStatus returns the HTTP status of one tool call.
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

// parseSearchPage reads result IDs, the next cursor, and completion.
func parseSearchPage(text string) (searchPage, error) {
	page := searchPage{IDs: []uuid.UUID{}, Cursor: "", Complete: strings.Contains(text, "The search has no more results.")}
	for _, line := range strings.Split(text, "\n") {
		if marker := strings.LastIndex(line, "Raw id: `"); marker >= 0 {
			value := strings.TrimSuffix(line[marker+len("Raw id: `"):], "`")
			id, err := uuid.Parse(value)
			if err != nil {
				return searchPage{}, fmt.Errorf("parse result id %q: %w", value, err)
			}
			page.IDs = append(page.IDs, id)
		}
		if strings.HasPrefix(line, "Next cursor: `") {
			page.Cursor = strings.TrimSuffix(strings.TrimPrefix(line, "Next cursor: `"), "`")
		}
	}
	if page.Complete == (page.Cursor != "") {
		return searchPage{}, fmt.Errorf("page must have exactly one of a cursor or completion:\n%s", text)
	}
	return page, nil
}

// callEverySearchPage follows every cursor, alternating between harnesses
// when more than one is given, until the response reports completion.
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
