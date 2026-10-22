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
// RequestRecorder saves the final read events before writing the buffered response.
// responseCapture does not implement [http.Flusher].
type responseCapture struct {
	audit.ReadValueMarker

	header http.Header
	status int
	body   bytes.Buffer
}

func newResponseCapture() *responseCapture {
	return &responseCapture{
		ReadValueMarker: audit.ReadValueMarker{}, header: http.Header{}, status: http.StatusOK, body: bytes.Buffer{},
	}
}

func (capture *responseCapture) Header() http.Header { return capture.header }

func (capture *responseCapture) Write(payload []byte) (int, error) {
	written, err := capture.body.Write(payload)
	if err != nil {
		return written, fmt.Errorf("capture response body: %w", err)
	}
	return written, nil
}

func (capture *responseCapture) WriteHeader(status int) { capture.status = status }

// readResult returns the buffered response and the targets that the handler collected.
// It first uses the outcome that a tool recorded in ctx. If that outcome is empty, it checks the
// response body for a JSON-RPC error, then checks the HTTP status if the outcome is still empty.
func (capture *responseCapture) readResult(ctx context.Context) audit.ReadResult[*responseCapture] {
	collected := audit.ReadCollected(ctx)
	outcome := collected.Outcome
	if outcome.Outcome == "" {
		outcome = jsonRPCOutcome(capture.header.Get("Content-Type"), capture.body.Bytes())
	}
	if outcome.Outcome == "" {
		outcome = statusOutcome(capture.status)
	}
	if outcome.Outcome == "" && capture.body.Len() == 0 && ctx.Err() != nil {
		outcome = failed("request_canceled", "request ended before response preparation")
	}
	return audit.ReadResult[*responseCapture]{
		Value: capture, Targets: collected.Targets, OrgTargets: collected.OrgTargets,
		Count: uint64(len(collected.Targets)),
		Encode: func(writer io.Writer, captured *responseCapture) error {
			if _, err := writer.Write(captured.body.Bytes()); err != nil {
				slog.ErrorContext(ctx, "mcp.audit_request.encode_failed", slog.String("err", err.Error()))
				return fmt.Errorf("write captured response body: %w", err)
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
		return audit.NoOutcome()
	case status == http.StatusBadRequest:
		return refused("invalid_request", "invalid request")
	case status == http.StatusUnauthorized:
		return refused("unauthenticated", "could not authenticate the request")
	case status == http.StatusForbidden:
		return refused("forbidden", "cannot perform the requested operation without permission")
	case status == http.StatusNotFound:
		return refused("not_found", "cannot find the requested route")
	case status == http.StatusMethodNotAllowed:
		return refused("method_not_allowed", "unsupported HTTP method for the route")
	case status < http.StatusInternalServerError:
		return refused("request_rejected", "cannot accept the request")
	case status == http.StatusServiceUnavailable:
		return failed("service_unavailable", "could not serve the request")
	default:
		return failed("server_error", "could not complete the request")
	}
}

// jsonRPCOutcome returns an audit outcome when a JSON response body contains a JSON-RPC error.
// It returns an empty outcome otherwise.
func jsonRPCOutcome(contentType string, body []byte) audit.OutcomeDetails {
	if !strings.HasPrefix(contentType, "application/json") {
		return audit.NoOutcome()
	}
	var response struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil || response.Error == nil {
		return audit.NoOutcome()
	}

	switch response.Error.Code {
	case jsonRPCParseError:
		return refused("parse_error", "invalid JSON-RPC request body")
	case jsonRPCInvalidRequest:
		return refused("invalid_request", "invalid JSON-RPC request")
	case jsonRPCMethodNotFound:
		return refused("method_not_found", "unsupported method or tool")
	case jsonRPCInvalidParams:
		return refused("invalid_params", "invalid request parameters")
	case jsonRPCInternalError:
		return failed("internal_error", "could not complete the MCP request")
	default:
		return refused("jsonrpc_error", "request failed with a JSON-RPC error")
	}
}

func refused(code, message string) audit.OutcomeDetails {
	return audit.OutcomeDetails{Outcome: audit.OutcomeRefused, FailureCode: code, FailureMessage: message, CommittedSteps: nil}
}

func failed(code, message string) audit.OutcomeDetails {
	return audit.OutcomeDetails{Outcome: audit.OutcomeFailed, FailureCode: code, FailureMessage: message, CommittedSteps: nil}
}
