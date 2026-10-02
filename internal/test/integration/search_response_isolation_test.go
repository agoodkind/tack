package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// responseIsolationQuery matches the caller's nodes and, more strongly,
	// the other organization's nodes.
	responseIsolationQuery = "harbor lantern"
	// foreignMarker appears only in the other organization's names and text.
	foreignMarker = "zephyrine"
)

// searchResponseLine matches every line a tack_search page may contain: the
// heading, a blank line, a result, the empty-page line, the completion line,
// and the cursor line. A count line or another field matches none of them.
var searchResponseLine = regexp.MustCompile("^(#### Search results|" +
	"|- .+ \\(.+\\) - Raw id: `[0-9a-f-]{36}`" +
	"|- This page contains no authorized matches\\." +
	"|The search has no more results\\." +
	"|Next cursor: `[^`]+`)$")

// searchResponseCountKeys lists JSON keys for a count value. The response
// body must contain none of them.
var searchResponseCountKeys = []string{`"total"`, `"count"`, `"hits"`, `"matches"`}

// rawSearchBody calls tack_search like rawSearchCall and returns the whole
// HTTP response body with the tool text.
func rawSearchBody(t *testing.T, harness *MCPHarness, arguments map[string]any) ([]byte, string) {
	t.Helper()
	requestID := uuid.NewString()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": requestID, "method": "tools/call",
		"params": map[string]any{"name": "tack_search", "arguments": arguments},
	})
	if err != nil {
		t.Fatalf("encode search request: %v", err)
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
	whole, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("search HTTP status %d: %v %s", response.StatusCode, err, whole)
	}
	payload := whole
	for _, line := range bytes.Split(whole, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data: ")) {
			payload = bytes.TrimPrefix(line, []byte("data: "))
		}
	}
	var decoded struct {
		Result rawToolResult `json:"result"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode search response: %v", err)
	}
	if decoded.Result.IsError {
		t.Fatalf("search returned an error: %s", toolResultText(decoded.Result))
	}
	return whole, toolResultText(decoded.Result)
}

// TestSearchResponseOmitsForeignOrganization stores caller nodes and stronger
// matching nodes in another organization. The other organization's caller
// finds its own nodes. Every response body the caller receives over the full
// traversal must contain no foreign node ID, no foreign organization or entry
// ID, no foreign text, no count key, and only the page lines tack_search
// renders.
func TestSearchResponseOmitsForeignOrganization(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	caller, foreign := fixture.Workspaces[0], otherOrganization(t, fixture)
	callerEntry, foreignEntry := entryPoint(t, fixture, caller), entryPoint(t, fixture, foreign)
	callerKind, foreignKind := putOpaqueKind(t, fixture, caller.OrgID), putOpaqueKind(t, fixture, foreign.OrgID)
	own := make([]uuid.UUID, 0, 30)
	for number := range 30 {
		own = append(own, putOpaqueNode(t, fixture, callerKind, callerEntry,
			fmt.Sprintf("Harbor lantern %d", number), "harbor lantern notes", "excluded"))
	}
	strong := strings.Repeat("Harbor lantern "+foreignMarker+" harbor lantern. ", 3)
	foreignIDs := make([]uuid.UUID, 0, 40)
	for number := range 40 {
		foreignIDs = append(foreignIDs, putOpaqueNode(t, fixture, foreignKind, foreignEntry,
			fmt.Sprintf("Zephyrine harbor lantern %d", number), strong, "excluded"))
	}
	drainSearchWork(t, fixture.Worker, 2000)
	foreignCaller := actorHarness(fixture, foreign, 0)
	requireCorpusOnce(t, callEverySearchPage(t, responseIsolationQuery, foreignCaller).IDs, foreignIDs, foreignEntry)

	forbidden := []string{foreignMarker, foreign.OrgID.String(), foreignEntry.String()}
	for _, id := range foreignIDs {
		forbidden = append(forbidden, id.String())
	}
	forbidden = append(forbidden, searchResponseCountKeys...)
	var seen []uuid.UUID
	cursor := ""
	for pageNumber := range 100 {
		whole, text := rawSearchBody(t, fixture.Harness, searchArguments(fixture.Harness, responseIsolationQuery, cursor))
		lowered := strings.ToLower(string(whole))
		for _, value := range forbidden {
			if strings.Contains(lowered, strings.ToLower(value)) {
				t.Fatalf("page %d response body contains %q:\n%s", pageNumber, value, whole)
			}
		}
		for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			if !searchResponseLine.MatchString(line) {
				t.Fatalf("page %d contains a line outside the page grammar: %q", pageNumber, line)
			}
		}
		page, err := parseSearchPage(text)
		if err != nil {
			t.Fatalf("parse page %d: %v", pageNumber, err)
		}
		seen = append(seen, page.IDs...)
		if page.Complete {
			requireCorpusOnce(t, seen, own, callerEntry)
			return
		}
		cursor = page.Cursor
	}
	t.Fatal("the caller's search did not complete within 100 pages")
}
