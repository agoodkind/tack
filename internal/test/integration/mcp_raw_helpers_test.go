package integration

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

type rawRPCResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type rawToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

var rawRequestCounter atomic.Int64

func callToolSuccess(t *testing.T, harness *MCPHarness, toolName string, arguments map[string]any) string {
	t.Helper()
	result := callToolRaw(t, harness, toolName, arguments)
	if result.IsError {
		t.Fatalf("%s returned an error: %s", toolName, toolResultText(result))
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("%s content = %+v, want one text item", toolName, result.Content)
	}
	return result.Content[0].Text
}

func callToolRaw(t *testing.T, harness *MCPHarness, toolName string, arguments map[string]any) rawToolResult {
	t.Helper()
	payload := callRPCRaw(t, harness, "tools/call", map[string]any{
		"name": toolName, "arguments": arguments,
	})
	var result rawToolResult
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatalf("decode %s result: %v\n%s", toolName, err, payload)
	}
	return result
}

func callRPCRaw(t *testing.T, harness *MCPHarness, method string, params map[string]any) json.RawMessage {
	t.Helper()
	requestID := "outage-acceptance-" + strconv.FormatInt(rawRequestCounter.Add(1), 10)
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": requestID, "method": method, "params": params,
	})
	if err != nil {
		t.Fatalf("marshal %s request: %v", method, err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+harness.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	recorder := httptest.NewRecorder()
	harness.handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("%s HTTP status = %d, body = %s", method, response.StatusCode, payload)
	}
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s response: %v", method, err)
	}
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		payload = lastSSEPayload(t, payload)
	}
	var decoded rawRPCResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode %s response: %v\n%s", method, err, payload)
	}
	if decoded.Error != nil {
		t.Fatalf("%s protocol error: %s", method, decoded.Error.Message)
	}
	return decoded.Result
}

func rawNodeID(t *testing.T, text string) string {
	t.Helper()
	const marker = "- Raw id: `"
	start := strings.LastIndex(text, marker)
	if start == -1 {
		t.Fatalf("tool output has no raw node id:\n%s", text)
	}
	remainder := text[start+len(marker):]
	end := strings.IndexByte(remainder, '`')
	if end == -1 {
		t.Fatalf("tool output has an unterminated raw node id:\n%s", text)
	}
	id := remainder[:end]
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("raw node id %q is invalid: %v", id, err)
	}
	return id
}

func toolResultText(result rawToolResult) string {
	texts := make([]string, 0, len(result.Content))
	for _, content := range result.Content {
		texts = append(texts, content.Text)
	}
	return strings.Join(texts, "\n")
}

func lastSSEPayload(t *testing.T, body []byte) []byte {
	t.Helper()
	var payload []byte
	for _, line := range bytes.Split(body, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data: ")) {
			payload = bytes.TrimPrefix(line, []byte("data: "))
		}
	}
	if len(payload) == 0 {
		t.Fatalf("SSE response contains no data event:\n%s", body)
	}
	return payload
}
