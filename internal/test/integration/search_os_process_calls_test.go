package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/telemetry"
)

type actualSearchToolRefusal struct{}

func (actualSearchToolRefusal) Error() string { return "actual process search tool rejected request" }

func actualProcessSearch(t *testing.T, server *actualSearchServer, harness *MCPHarness, sessionID, query, cursor string) (searchPage, error) {
	t.Helper()
	requestID := uuid.NewString()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": requestID, "method": "tools/call",
		"params": map[string]any{"name": "tack_search", "arguments": searchArguments(harness, query, cursor)},
	})
	if err != nil {
		return searchPage{}, err
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.endpoint+"/mcp", bytes.NewReader(body))
	if err != nil {
		return searchPage{}, err
	}
	request.Header.Set("Authorization", "Bearer "+harness.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Mcp-Session-Id", sessionID)
	request.Header.Set(telemetry.RequestIDHeader, requestID)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return searchPage{}, fmt.Errorf("actual process HTTP request failed")
	}
	const responseLimit = 8388608
	payload, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	closeError := response.Body.Close()
	if err != nil || closeError != nil || len(payload) > responseLimit {
		return searchPage{}, fmt.Errorf("actual process response body failed")
	}
	if response.StatusCode != http.StatusOK {
		return searchPage{}, fmt.Errorf("actual process HTTP status %d", response.StatusCode)
	}
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		for _, line := range bytes.Split(payload, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("data: ")) {
				payload = bytes.TrimPrefix(line, []byte("data: "))
			}
		}
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Result  datagen.Result  `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.JSONRPC != "2.0" || envelope.ID != requestID || len(envelope.Error) != 0 {
		return searchPage{}, fmt.Errorf("actual process JSON-RPC response failed")
	}
	if envelope.Result.IsError {
		return searchPage{}, actualSearchToolRefusal{}
	}
	if err := requireSearchAuditInvocation(t.Context(), harness, requestID); err != nil {
		return searchPage{}, err
	}
	page, err := parseSearchPage(envelope.Result.Text())
	if err == nil {
		t.Logf("actual public request pid=%d request_id=%s results=%d complete=%t", server.command.Process.Pid, requestID, len(page.IDs), page.Complete)
	}
	return page, err
}
