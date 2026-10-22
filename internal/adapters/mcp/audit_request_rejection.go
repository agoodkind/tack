package mcp

import (
	"net/http"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
)

const (
	// mcpMethodNotAllowedBody is the response body for a GET request to the MCP endpoint.
	mcpMethodNotAllowedBody = "method not allowed: this endpoint requires POST\n"
	// healthMethodNotAllowedBody is the response body when a health check request does not use GET or HEAD.
	healthMethodNotAllowedBody = "method not allowed: the health check accepts GET and HEAD\n"
	// organizationUnavailableBody is the response body when the server cannot read the organization of a request.
	organizationUnavailableBody = `{"error":"organization unavailable"}`
)

// methodNotAllowed writes a 405 response with the allowed methods and body.
func methodNotAllowed(writer http.ResponseWriter, allow, body string) {
	writer.Header().Set("Allow", allow)
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(http.StatusMethodNotAllowed)
	_, _ = writer.Write([]byte(body))
}

// organizationUnavailable returns the 503 response for a request when the server
// cannot read the organization of the request.
func organizationUnavailable() *responseCapture {
	capture := newResponseCapture()
	capture.Header().Set("Content-Type", "application/json")
	capture.WriteHeader(http.StatusServiceUnavailable)
	_, _ = capture.Write([]byte(organizationUnavailableBody))
	return capture
}

// rejectionVerb returns the verb of a request that authentication rejected.
func rejectionVerb(reason string) audit.Verb {
	if reason == auth.RejectionMissingAuthorization {
		return audit.VerbAuthLoginFailed
	}
	return audit.VerbAuthTokenRejected
}

// rejectionOutcome returns the outcome of a request that authentication rejected.
func rejectionOutcome(reason string) audit.OutcomeDetails {
	switch reason {
	case auth.RejectionMissingAuthorization:
		return refused(reason, "missing bearer token")
	case auth.RejectionBearerRefused:
		return refused(reason, "bearer token rejection by the token store")
	case auth.RejectionDevIdentityInvalid:
		return refused(reason, "invalid user ID in the development bearer token")
	case auth.RejectionBearerCheckFailed:
		return failed(reason, "could not check the bearer token")
	default:
		return refused("authentication_rejected", "request rejection during authentication")
	}
}
