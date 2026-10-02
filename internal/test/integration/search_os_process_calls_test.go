package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/telemetry"
)

// actualToolError is an MCP tool result with isError set. Text is the raw tool
// text for refusal classification; Message is the sanitized text for output.
type actualToolError struct {
	Text    string
	Message string
}

func (failure actualToolError) Error() string {
	return fmt.Sprintf("actual process tool error text=%s", failure.Message)
}

// actualHTTPStatusError is an MCP request that returned a non-200 HTTP status.
type actualHTTPStatusError struct {
	StatusCode int
}

func (failure actualHTTPStatusError) Error() string {
	return fmt.Sprintf("actual process HTTP status %d", failure.StatusCode)
}

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
	Result    datagen.Result
	RawResult json.RawMessage
	ID        string
	Latency   time.Duration
}

func actualProcessTool(t *testing.T, server *actualSearchServer, harness *MCPHarness, sessionID, name string, arguments map[string]any) (actualToolCall, error) {
	call, err := actualProcessRPC(t, server, harness, sessionID, "tools/call", map[string]any{"name": name, "arguments": arguments})
	if err != nil {
		return call, err
	}
	if err := json.Unmarshal(call.RawResult, &call.Result); err != nil {
		return call, fmt.Errorf("actual process tool result failed to decode")
	}
	if call.Result.IsError {
		message := sanitizeActualProtocolMessage(call.Result.Text(), server.command.Env, harness.token)
		t.Logf("actual process tool error request_id=%s tool=%s message=%s", call.ID, name, message)
		return call, actualToolError{Text: call.Result.Text(), Message: message}
	}
	if err := requireActualProcessAudit(t.Context(), harness, call.ID, name); err != nil {
		return call, err
	}
	return call, nil
}

type actualProtocolError struct {
	Code    int
	Message string
}

func (failure actualProtocolError) Error() string {
	return fmt.Sprintf("actual process JSON-RPC code=%d message=%s", failure.Code, failure.Message)
}

func actualProcessRPC(t *testing.T, server *actualSearchServer, harness *MCPHarness, sessionID, method string, parameters map[string]any) (actualToolCall, error) {
	t.Helper()
	var call actualToolCall
	requestID := uuid.NewString()
	call.ID = requestID
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": requestID, "method": method,
		"params": parameters,
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
		return call, actualHTTPStatusError{StatusCode: response.StatusCode}
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
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return call, fmt.Errorf("actual process JSON-RPC decoding failed request_id=%s status=%d bytes=%d", requestID, response.StatusCode, len(payload))
	}
	if envelope.JSONRPC != "2.0" || envelope.ID != requestID {
		return call, fmt.Errorf("actual process JSON-RPC identity failed request_id=%s status=%d", requestID, response.StatusCode)
	}
	if len(envelope.Error) != 0 && string(envelope.Error) != "null" {
		var failure struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(envelope.Error, &failure); err != nil {
			return call, fmt.Errorf("actual process JSON-RPC error failed to decode request_id=%s", requestID)
		}
		message := sanitizeActualProtocolMessage(failure.Message, server.command.Env, harness.token)
		keys := make([]string, 0)
		if arguments, ok := parameters["arguments"].(map[string]any); ok {
			for key := range arguments {
				keys = append(keys, key)
			}
		}
		slices.Sort(keys)
		t.Logf("actual process protocol error status=%d request_id=%s method=%s tool=%v argument_keys=%v code=%d message=%s", response.StatusCode, requestID, method, parameters["name"], keys, failure.Code, message)
		return call, actualProtocolError{Code: failure.Code, Message: message}
	}
	call.RawResult = envelope.Result
	return call, nil
}
