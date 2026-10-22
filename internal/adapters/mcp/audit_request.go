// Package mcp serves the MCP endpoint and records HTTP read and rejection events.
package mcp

import (
	"net/http"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
)

// The request recorder records requests to these routes outside the MCP endpoint.
const (
	healthRoute      = "/healthz"
	diagnosticsRoute = "/debug/vars"
	unknownRoute     = "/"
)

// RequestRecorder buffers HTTP read and rejection responses and saves the final event before writing them.
// FoundationDB writers record changes from authenticated MCP tool calls that change state.
type RequestRecorder struct {
	store audit.ReadLifecycle
}

// NewRequestRecorder returns a recorder that saves read events in store.
func NewRequestRecorder(store audit.ReadLifecycle) *RequestRecorder {
	return &RequestRecorder{store: store}
}

// Health records each request to the health check route.
func (recorder *RequestRecorder) Health(next http.Handler) http.Handler {
	return recorder.route(routeOperation(audit.VerbHTTPHealthRead, healthRoute), next)
}

// Diagnostics records each request to the diagnostics route.
func (recorder *RequestRecorder) Diagnostics(next http.Handler) http.Handler {
	return recorder.route(routeOperation(audit.VerbHTTPDiagnosticsRead, diagnosticsRoute), next)
}

// UnknownRoute records each request to a route that the server does not
// register and answers with 404.
func (recorder *RequestRecorder) UnknownRoute() http.Handler {
	return recorder.route(routeOperation(audit.VerbHTTPRouteUnknown, unknownRoute), http.NotFoundHandler())
}

// route records each request to a route that does not authenticate. The event
// uses the unauthenticated actor and the system organization.
func (recorder *RequestRecorder) route(operation requestOperation, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorder.serveRead(writer, request, newRequestRead(request.Context(), operation), next)
	})
}

// MCP rejects GET requests before authentication and records authentication rejections with their reason.
// It uses the unauthenticated actor and the system organization for these rejections.
// It saves a read admission before running next for authenticated requests that do not change state.
func (recorder *RequestRecorder) MCP(authenticate func(http.Handler) http.Handler, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		operation := mcpOperation(request)
		ctx := auth.WithRejectionNote(request.Context())
		read := newRequestRead(ctx, operation)
		ctx = audit.WithOpID(ctx, read.operationID)
		request = request.WithContext(ctx)
		if request.Method == http.MethodGet {
			recorder.rejectBeforeAuthentication(writer, request, read, methodNotAllowed())
			return
		}
		handled := false
		authenticated := http.HandlerFunc(func(_ http.ResponseWriter, authenticatedRequest *http.Request) {
			handled = true
			recorder.serveAuthenticated(writer, authenticatedRequest, read, next)
		})
		capture := newResponseCapture()
		authenticate(authenticated).ServeHTTP(capture, request)
		if handled {
			return
		}
		recorder.rejectBeforeAuthentication(writer, request, read, capture)
	})
}

// serveAuthenticated runs next without recording a read event if the authenticated tool call changes state.
// For other requests, it records the verified user and uses actorOrganization to select the organization.
func (recorder *RequestRecorder) serveAuthenticated(writer http.ResponseWriter, request *http.Request, read requestRead, next http.Handler) {
	if read.operation.mutation {
		next.ServeHTTP(writer, request)
		return
	}
	userID, _ := auth.UserID(request.Context())
	read.actor = userActor(request.Context(), userID)
	read.orgID = actorOrganization(request.Context())
	recorder.serveRead(writer, request, read, next)
}

// rejectBeforeAuthentication saves a refused or failed read event before writing a response
// that the server prepared before authentication succeeded.
func (recorder *RequestRecorder) rejectBeforeAuthentication(writer http.ResponseWriter, request *http.Request, read requestRead, capture *responseCapture) {
	reason, rejected := auth.RejectionReason(request.Context())
	if rejected {
		read.operation.verb = rejectionVerb(reason)
	}
	result := capture.readResult(request.Context())
	result.Targets, result.Count = nil, 0
	if rejected {
		result.Outcome = rejectionOutcome(reason)
	}
	recorder.release(writer, request, read, func(*http.Request) audit.ReadResult[*responseCapture] { return result })
}

// methodNotAllowed returns the 405 response for a GET request to the MCP endpoint.
func methodNotAllowed() *responseCapture {
	capture := newResponseCapture()
	capture.Header().Set("Allow", http.MethodPost)
	capture.Header().Set("Content-Type", "text/plain; charset=utf-8")
	capture.WriteHeader(http.StatusMethodNotAllowed)
	_, _ = capture.Write([]byte("method not allowed: streamable-http stateless mode requires POST\n"))
	return capture
}
