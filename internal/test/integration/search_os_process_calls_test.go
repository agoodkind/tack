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
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/telemetry"
)

type actualSearchToolRefusal struct{}

func (actualSearchToolRefusal) Error() string { return "actual process search tool rejected request" }

func actualProcessSearch(t *testing.T, server *actualSearchServer, harness *MCPHarness, sessionID, query, cursor string) (searchPage, error) {
	t.Helper()
	call, err := actualProcessTool(t, server, harness, sessionID, "tack_search", searchArguments(harness, query, cursor))
	if err != nil {
		return searchPage{}, err
	}
	page, err := parseSearchPage(call.Result.Text())
	if err == nil {
		t.Logf("actual public request pid=%d request_id=%s results=%d complete=%t", server.command.Process.Pid, call.ID, len(page.IDs), page.Complete)
	}
	return page, err
}

type actualToolCall struct {
	Result  datagen.Result
	ID      string
	Latency time.Duration
}

func actualProcessTool(t *testing.T, server *actualSearchServer, harness *MCPHarness, sessionID, name string, arguments map[string]any) (actualToolCall, error) {
	t.Helper()
	var call actualToolCall
	requestID := uuid.NewString()
	call.ID = requestID
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": requestID, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": arguments},
	})
	if err != nil {
		return call, err
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.endpoint+"/mcp", bytes.NewReader(body))
	if err != nil {
		return call, err
	}
	request.Header.Set("Authorization", "Bearer "+harness.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Mcp-Session-Id", sessionID)
	request.Header.Set(telemetry.RequestIDHeader, requestID)
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	started := clock.Now()
	response, err := client.Do(request)
	if err != nil {
		call.Latency = clock.Since(started)
		return call, fmt.Errorf("actual process HTTP request failed")
	}
	const responseLimit = 8388608
	payload, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	closeError := response.Body.Close()
	call.Latency = clock.Since(started)
	if err != nil || closeError != nil || len(payload) > responseLimit {
		return call, fmt.Errorf("actual process response body failed")
	}
	if response.StatusCode != http.StatusOK {
		return call, fmt.Errorf("actual process HTTP status %d", response.StatusCode)
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
		return call, fmt.Errorf("actual process JSON-RPC response failed")
	}
	if envelope.Result.IsError {
		return call, actualSearchToolRefusal{}
	}
	if err := requireSearchAuditInvocation(t.Context(), harness, requestID); err != nil {
		return call, err
	}
	call.Result = envelope.Result
	return call, nil
}
