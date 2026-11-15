package mcp

import (
	"bytes"
	"fmt"
	"net/http"

	"goodkind.io/tack/internal/audit"
)

// responseCapture buffers the status, headers, and body that a handler writes.
// RequestRecorder saves a security refusal before releasing the response.
// responseCapture does not implement [http.Flusher].
type responseCapture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newResponseCapture() *responseCapture {
	return &responseCapture{
		header: http.Header{},
		status: http.StatusOK,
		body:   bytes.Buffer{},
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

// statusOutcome identifies HTTP authentication and authorization decisions.
func statusOutcome(status int) audit.OutcomeDetails {
	switch status {
	case http.StatusUnauthorized:
		return refused("unauthenticated", "could not authenticate the request")
	case http.StatusForbidden:
		return refused("forbidden", "cannot perform the requested operation without permission")
	default:
		return audit.NoOutcome()
	}
}

func refused(code, message string) audit.OutcomeDetails {
	return audit.OutcomeDetails{
		Outcome:        audit.OutcomeRefused,
		FailureCode:    code,
		FailureMessage: message,
		CommittedSteps: nil,
	}
}

func failed(code, message string) audit.OutcomeDetails {
	return audit.OutcomeDetails{
		Outcome:        audit.OutcomeFailed,
		FailureCode:    code,
		FailureMessage: message,
		CommittedSteps: nil,
	}
}
