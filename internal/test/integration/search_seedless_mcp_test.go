package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/telemetry"
)

const (
	seedlessRawIDMarker   = " - Raw id: `"
	seedlessCursorPrefix  = "Next cursor: `"
	seedlessCompleteText  = "The search has no more results."
	seedlessSearchPages   = 100
	seedlessEventDataLine = "data: "
)

type seedlessRequest struct {
	JSONRPC string             `json:"jsonrpc"`
	ID      string             `json:"id"`
	Method  string             `json:"method"`
	Params  seedlessToolParams `json:"params"`
}

type seedlessToolParams struct {
	Name      string                     `json:"name"`
	Arguments map[string]json.RawMessage `json:"arguments"`
}

type seedlessResponse struct {
	Result seedlessToolResult `json:"result"`
	Error  *seedlessRPCError  `json:"error"`
}

type seedlessRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type seedlessToolResult struct {
	Content []seedlessContent `json:"content"`
	IsError bool              `json:"isError"`
}

type seedlessContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type seedlessResult struct {
	Name string
	Type string
	ID   uuid.UUID
}

func seedlessString(value string) json.RawMessage {
	return json.RawMessage(strconv.Quote(value))
}

func callSeedlessTool(t *testing.T, env seedlessEnvironment, caller seedlessOrg, tool string, arguments map[string]json.RawMessage) string {
	t.Helper()
	requestID := uuid.NewString()
	body, err := json.Marshal(seedlessRequest{
		JSONRPC: "2.0", ID: requestID, Method: "tools/call",
		Params: seedlessToolParams{Name: tool, Arguments: arguments},
	})
	if err != nil {
		t.Fatalf("encode %s request: %v", tool, err)
	}
	ctx := telemetry.WithRequestMetadata(t.Context(), requestID)
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp", bytes.NewReader(body))
	request.Header.Set(telemetry.RequestIDHeader, requestID)
	request.Header.Set("Authorization", "Bearer "+caller.Token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	recorder := httptest.NewRecorder()
	env.Graph.MCPRoute.ServeHTTP(recorder, request)
	payload := recorder.Body.Bytes()
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s returned HTTP %d: %s", tool, recorder.Code, payload)
	}
	if strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") {
		for _, line := range bytes.Split(payload, []byte("\n")) {
			if bytes.HasPrefix(line, []byte(seedlessEventDataLine)) {
				payload = bytes.TrimPrefix(line, []byte(seedlessEventDataLine))
			}
		}
	}
	var decoded seedlessResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode %s response %s: %v", tool, payload, err)
	}
	if decoded.Error != nil {
		t.Fatalf("%s returned JSON-RPC error %d: %s", tool, decoded.Error.Code, decoded.Error.Message)
	}
	parts := make([]string, 0, len(decoded.Result.Content))
	for _, content := range decoded.Result.Content {
		parts = append(parts, content.Text)
	}
	text := strings.Join(parts, "\n")
	if decoded.Result.IsError {
		t.Fatalf("%s returned an error result: %s", tool, text)
	}
	return text
}

func seedlessRawID(t *testing.T, text string) uuid.UUID {
	t.Helper()
	marker := strings.Index(text, "Raw id: `")
	if marker < 0 {
		t.Fatalf("response has no raw id:\n%s", text)
	}
	value := text[marker+len("Raw id: `"):]
	end := strings.IndexByte(value, '`')
	if end < 0 {
		t.Fatalf("response has an unterminated raw id:\n%s", text)
	}
	id, err := uuid.Parse(value[:end])
	if err != nil {
		t.Fatalf("parse raw id %q: %v", value[:end], err)
	}
	return id
}

func searchSeedless(t *testing.T, env seedlessEnvironment, caller seedlessOrg, query, nodeType string) []seedlessResult {
	t.Helper()
	var results []seedlessResult
	cursor := ""
	for range seedlessSearchPages {
		arguments := map[string]json.RawMessage{
			caller.Keys.Entry + "_reference": seedlessString(caller.EntryAddress), "query": seedlessString(query),
		}
		if nodeType != "" {
			arguments["node_type"] = seedlessString(nodeType)
		}
		if cursor != "" {
			arguments["cursor"] = seedlessString(cursor)
		}
		text := callSeedlessTool(t, env, caller, "tack_search", arguments)
		cursor = ""
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, seedlessCursorPrefix) {
				cursor = strings.TrimSuffix(strings.TrimPrefix(line, seedlessCursorPrefix), "`")
			}
			if strings.HasPrefix(line, "- ") && strings.Contains(line, seedlessRawIDMarker) {
				results = append(results, parseSeedlessResult(t, line))
			}
		}
		complete := strings.Contains(text, seedlessCompleteText)
		if complete == (cursor != "") {
			t.Fatalf("search %q page must have exactly one of a cursor or completion:\n%s", query, text)
		}
		if complete {
			return results
		}
	}
	t.Fatalf("search %q did not complete within %d pages", query, seedlessSearchPages)
	return nil
}

func parseSeedlessResult(t *testing.T, line string) seedlessResult {
	t.Helper()
	marker := strings.LastIndex(line, seedlessRawIDMarker)
	head := strings.TrimPrefix(line[:marker], "- ")
	typeStart := strings.LastIndex(head, " (")
	if typeStart < 0 || !strings.HasSuffix(head, ")") {
		t.Fatalf("result line %q has no node type", line)
	}
	id, err := uuid.Parse(strings.TrimSuffix(line[marker+len(seedlessRawIDMarker):], "`"))
	if err != nil {
		t.Fatalf("parse result line %q: %v", line, err)
	}
	return seedlessResult{Name: head[:typeStart], Type: strings.TrimSuffix(head[typeStart+len(" ("):], ")"), ID: id}
}
