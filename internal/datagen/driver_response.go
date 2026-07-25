package datagen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxResponseBodyBytes = 8 << 20

// ToolContent is one MCP tool result content item.
type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Result is the MCP tools/call result used by the generator.
type Result struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type rpcError struct {
	Code     int    `json:"code"`
	Message  string `json:"message"`
	toolName string
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      string    `json:"id"`
	Result  Result    `json:"result"`
	Error   *rpcError `json:"error,omitempty"`
}

func decodeResponse(
	ctx context.Context,
	toolName string,
	response *http.Response,
) (Result, error) {
	payload, err := responsePayload(ctx, toolName, response)
	if err != nil {
		return Result{}, err
	}
	resultPayload, responseError, err := decodeRPCEnvelope(payload)
	if err != nil {
		return Result{}, loggedError(ctx, "qa datagen: decode "+toolName+" response", err)
	}
	if responseError != nil {
		responseError.toolName = toolName
		return Result{}, responseError
	}
	var result Result
	if err := json.Unmarshal(resultPayload, &result); err != nil {
		return Result{}, loggedError(ctx, "qa datagen: decode "+toolName+" result", err)
	}
	if result.IsError {
		return Result{}, &toolCallError{toolName: toolName, message: result.Text()}
	}
	return result, nil
}

func responsePayload(
	ctx context.Context,
	operation string,
	response *http.Response,
) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBodyBytes+1))
	closeErr := response.Body.Close()
	if err != nil {
		return nil, loggedError(ctx, "qa datagen: read "+operation+" response", err)
	}
	if closeErr != nil {
		return nil, loggedError(ctx, "qa datagen: close "+operation+" response", closeErr)
	}
	if len(body) > maxResponseBodyBytes {
		return nil, loggedError(
			ctx,
			"qa datagen: read "+operation+" response",
			fmt.Errorf("response exceeds %d-byte limit", maxResponseBodyBytes),
		)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, &httpStatusError{
			operation:  operation,
			statusCode: response.StatusCode,
			body:       strings.TrimSpace(string(body)),
		}
	}
	payload := body
	if strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		payload, err = readLastSSEData(body)
		if err != nil {
			return nil, loggedError(ctx, "qa datagen: read "+operation+" SSE response", err)
		}
	}
	return payload, nil
}

// Text returns all text content in order.
func (r Result) Text() string {
	parts := make([]string, 0, len(r.Content))
	for _, content := range r.Content {
		if content.Text != "" {
			parts = append(parts, content.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// RawID reads the rendered Raw id field from a node result.
func (r Result) RawID() string {
	const marker = "Raw id: `"
	text := r.Text()
	start := strings.Index(text, marker)
	if start < 0 {
		return ""
	}
	value := text[start+len(marker):]
	end := strings.IndexByte(value, '`')
	if end < 0 {
		return ""
	}
	return value[:end]
}

// ReferenceForName reads a list item's printed reference for an exact name.
func (r Result) ReferenceForName(name string) string {
	lines := strings.Split(r.Text(), "\n")
	target := "  - Name: " + name
	for index, line := range lines {
		if line != target || index == 0 {
			continue
		}
		reference := strings.TrimSpace(strings.TrimPrefix(lines[index-1], "- "))
		return strings.Trim(reference, "`")
	}
	return ""
}

func syntheticResult(requestID string) Result {
	return Result{Content: []ToolContent{{
		Type: "text",
		Text: "#### Planned node\n\n- Raw id: `" + deterministicUUID(requestID).String() + "`",
	}}}
}
