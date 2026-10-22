package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"goodkind.io/tack/internal/audit"
)

// The MCP server returns these JSON-RPC error codes.
const (
	jsonRPCParseError     = -32700
	jsonRPCInvalidRequest = -32600
	jsonRPCMethodNotFound = -32601
	jsonRPCInvalidParams  = -32602
	jsonRPCInternalError  = -32603
)

// responseCapture buffers the status, headers, and body that a handler writes.
// RequestRecorder saves the final read event before writing the buffered response.
// responseCapture does not implement [http.Flusher].
type responseCapture struct {
	audit.ReadValueMarker

	header http.Header
	status int
	body   bytes.Buffer
}

// newResponseCapture returns an empty capture with status 200.
func newResponseCapture() *responseCapture {
	return &responseCapture{
		ReadValueMarker: audit.ReadValueMarker{}, header: http.Header{}, status: http.StatusOK, body: bytes.Buffer{},
	}
}

// Header returns the captured response headers.
func (capture *responseCapture) Header() http.Header { return capture.header }

// Write appends payload to the captured body.
func (capture *responseCapture) Write(payload []byte) (int, error) {
	written, err := capture.body.Write(payload)
	if err != nil {
		return written, fmt.Errorf("the server could not capture the response body: %w", err)
	}
	return written, nil
}

// WriteHeader stores the response status.
func (capture *responseCapture) WriteHeader(status int) { capture.status = status }

// readResult returns the buffered response and the targets that the handler collected.
// It first uses the outcome that a tool recorded in ctx. If that outcome is empty, it checks the
// response body for a JSON-RPC error, then checks the HTTP status if the outcome is still empty.
func (capture *responseCapture) readResult(ctx context.Context) audit.ReadResult[*responseCapture] {
	targets, outcome := audit.CollectedRead(ctx)
	if outcome.Outcome == "" {
		outcome = jsonRPCOutcome(capture.header.Get("Content-Type"), capture.body.Bytes())
	}
	if outcome.Outcome == "" {
		outcome = statusOutcome(capture.status)
	}
	if outcome.Outcome == "" && capture.body.Len() == 0 && ctx.Err() != nil {
		outcome = failed("request_canceled", "the request ended before the server prepared a response")
	}
	return audit.ReadResult[*responseCapture]{
		Value: capture, Targets: targets, Count: uint64(len(targets)),
		Encode: func(writer io.Writer, captured *responseCapture) error {
			if _, err := writer.Write(captured.body.Bytes()); err != nil {
				slog.ErrorContext(ctx, "mcp.audit_request.encode_failed", slog.String("err", err.Error()))
				return fmt.Errorf("the server could not write the captured response body: %w", err)
			}
			return nil
		},
		ContentType: capture.header.Get("Content-Type"), Outcome: outcome,
	}
}

// release writes the captured status, headers, and response payload.
func (capture *responseCapture) release(writer http.ResponseWriter, payload []byte) {
	for name, values := range capture.header {
		if name == "Content-Length" {
			continue
		}
		writer.Header()[name] = values
	}
	writer.WriteHeader(capture.status)
	_, _ = writer.Write(payload)
}

// statusOutcome returns an audit outcome for HTTP error statuses and an empty outcome for other statuses.
func statusOutcome(status int) audit.OutcomeDetails {
	switch {
	case status < http.StatusBadRequest:
		return noOutcome()
	case status == http.StatusBadRequest:
		return refused("invalid_request", "the server rejected the request as invalid")
	case status == http.StatusUnauthorized:
		return refused("unauthenticated", "the server did not authenticate the request")
	case status == http.StatusForbidden:
		return refused("forbidden", "the caller does not have permission to perform the requested operation")
	case status == http.StatusNotFound:
		return refused("not_found", "the server does not register the requested route")
	case status == http.StatusMethodNotAllowed:
		return refused("method_not_allowed", "the route does not accept the HTTP method")
	case status < http.StatusInternalServerError:
		return refused("request_rejected", "the server rejected the request")
	case status == http.StatusServiceUnavailable:
		return failed("service_unavailable", "the server could not serve the request")
	default:
		return failed("server_error", "the server could not complete the request")
	}
}

// jsonRPCOutcome returns an audit outcome when a JSON response body contains a JSON-RPC error.
// It returns an empty outcome otherwise.
func jsonRPCOutcome(contentType string, body []byte) audit.OutcomeDetails {
	if !strings.HasPrefix(contentType, "application/json") {
		return noOutcome()
	}
	var response struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil || response.Error == nil {
		return noOutcome()
	}

	switch response.Error.Code {
	case jsonRPCParseError:
		return refused("parse_error", "the request body is not valid JSON-RPC")
	case jsonRPCInvalidRequest:
		return refused("invalid_request", "the request is not a valid JSON-RPC request")
	case jsonRPCMethodNotFound:
		return refused("method_not_found", "the MCP server does not provide the requested method or tool")
	case jsonRPCInvalidParams:
		return refused("invalid_params", "the request parameters are invalid")
	case jsonRPCInternalError:
		return failed("internal_error", "the MCP server could not complete the request")
	default:
		return refused("jsonrpc_error", "the MCP server returned a JSON-RPC error")
	}
}

// noOutcome returns outcome details without an outcome.
func noOutcome() audit.OutcomeDetails {
	return audit.OutcomeDetails{Outcome: "", FailureCode: "", FailureMessage: "", CommittedSteps: nil}
}

// refused returns a refused outcome with code and message.
func refused(code, message string) audit.OutcomeDetails {
	return audit.OutcomeDetails{Outcome: audit.OutcomeRefused, FailureCode: code, FailureMessage: message, CommittedSteps: nil}
}

// failed returns a failed outcome with code and message.
func failed(code, message string) audit.OutcomeDetails {
	return audit.OutcomeDetails{Outcome: audit.OutcomeFailed, FailureCode: code, FailureMessage: message, CommittedSteps: nil}
}
