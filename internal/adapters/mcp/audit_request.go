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

// RequestRecorder buffers HTTP read and rejection responses and saves final read events before writing responses.
// FoundationDB writers record changes from authenticated MCP tools that change state.
type RequestRecorder struct {
	store audit.ReadLifecycle
}

// NewRequestRecorder returns a recorder that saves read events in store.
func NewRequestRecorder(store audit.ReadLifecycle) *RequestRecorder {
	return &RequestRecorder{store: store}
}

// Health records each request to the health check route and returns 405 for methods other than GET or HEAD.
func (recorder *RequestRecorder) Health(next http.Handler) http.Handler {
	return recorder.route(routeOperation(audit.VerbHTTPHealthRead, healthRoute), http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet && request.Method != http.MethodHead {
				methodNotAllowed(writer, http.MethodGet+", "+http.MethodHead, healthMethodNotAllowedBody)
				return
			}
			next.ServeHTTP(writer, request)
		}))
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
// For an authenticated request that does not change state, it derives the organizations
// and saves one admission per organization before the handler reads data.
func (recorder *RequestRecorder) MCP(authenticate func(http.Handler) http.Handler, handler *Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		operation := mcpOperation(request)
		ctx := auth.WithRejectionNote(request.Context())
		read := newRequestRead(ctx, operation)
		ctx = audit.WithOpID(ctx, read.operationID)
		request = request.WithContext(ctx)
		if request.Method == http.MethodGet {
			capture := newResponseCapture()
			methodNotAllowed(capture, http.MethodPost, mcpMethodNotAllowedBody)
			recorder.rejectBeforeAuthentication(writer, request, read, capture)
			return
		}
		handled := false
		authenticated := http.HandlerFunc(func(_ http.ResponseWriter, authenticatedRequest *http.Request) {
			handled = true
			recorder.serveAuthenticated(writer, authenticatedRequest, read, handler)
		})
		capture := newResponseCapture()
		authenticate(authenticated).ServeHTTP(capture, request)
		if handled {
			return
		}
		recorder.rejectBeforeAuthentication(writer, request, read, capture)
	})
}

// serveAuthenticated runs handler without recording a read event if the authenticated tool call changes state.
// For other requests, it records the verified user and the organizations that
// Handler.requestOrganizations returns. When the server cannot derive the
// organizations, it records a failed read in the system organization and answers with 503.
func (recorder *RequestRecorder) serveAuthenticated(writer http.ResponseWriter, request *http.Request, read requestRead, handler *Handler) {
	if read.operation.mutation {
		handler.ServeHTTP(writer, request)
		return
	}
	ctx := request.Context()
	userID, _ := auth.UserID(ctx)
	read.actor = userActor(ctx, userID)
	orgIDs, err := handler.requestOrganizations(ctx, read.operation)
	if err != nil {
		result := organizationUnavailable().readResult(ctx)
		result.Outcome = failed("organization_unavailable", "could not read the request organization")
		recorder.release(writer, request, read, func(*http.Request) audit.ReadResult[*responseCapture] { return result })
		return
	}
	read.orgIDs = orgIDs
	if read.operation.metadata {
		request = request.WithContext(withMetadataTargets(ctx))
	}
	recorder.serveRead(writer, request, read, handler)
}

// rejectBeforeAuthentication saves a refused or failed read event before writing a response
// that the server prepared before authentication succeeded.
func (recorder *RequestRecorder) rejectBeforeAuthentication(writer http.ResponseWriter, request *http.Request, read requestRead, capture *responseCapture) {
	reason, rejected := auth.RejectionReason(request.Context())
	if rejected {
		read.operation.verb = rejectionVerb(reason)
	}
	result := capture.readResult(request.Context())
	result.Targets, result.OrgTargets, result.Count = nil, nil, 0
	if rejected {
		result.Outcome = rejectionOutcome(reason)
	}
	recorder.release(writer, request, read, func(*http.Request) audit.ReadResult[*responseCapture] { return result })
}
