// Package mcp serves the MCP endpoint and records HTTP reads and rejections.
package mcp

import (
	"net/http"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
)

const (
	healthRoute      = "/healthz"
	diagnosticsRoute = "/debug/vars"
	unknownRoute     = "/"
)

// RequestRecorder buffers HTTP read and rejection responses and saves final
// read events before writing responses. FoundationDB writers record changes
// from authenticated MCP tools that change state.
type RequestRecorder struct {
	store audit.ReadLifecycle
}

// NewRequestRecorder returns a recorder that saves read events in store.
//
// The recorder uses store for read admissions and final events.
//
//	recorder := NewRequestRecorder(store)
func NewRequestRecorder(store audit.ReadLifecycle) *RequestRecorder {
	return &RequestRecorder{store: store}
}

// Health records requests to the health check route.
//
// The wrapper checks the method before calling next. It saves the final read
// event before writing the response.
//
//   - GET and HEAD requests call next.
//   - Other methods receive HTTP 405.
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

// Diagnostics records requests to the diagnostics route.
//
// The wrapper saves the read admission before calling next and saves the
// final event before writing the response.
//
//	diagnostics := recorder.Diagnostics(next)
func (recorder *RequestRecorder) Diagnostics(next http.Handler) http.Handler {
	return recorder.route(routeOperation(audit.VerbHTTPDiagnosticsRead, diagnosticsRoute), next)
}

// UnknownRoute records requests to routes that the server does not register.
//
// The wrapper records each request before releasing the HTTP 404 response.
//
//	unknown := recorder.UnknownRoute()
func (recorder *RequestRecorder) UnknownRoute() http.Handler {
	return recorder.route(routeOperation(audit.VerbHTTPRouteUnknown, unknownRoute), http.NotFoundHandler())
}

// route records each request to a route that does not authenticate. The
// event uses the unauthenticated actor and the system organization.
func (recorder *RequestRecorder) route(operation requestOperation, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorder.serveRead(writer, request, newRequestRead(request.Context(), request, operation), next)
	})
}

// MCP rejects GET requests before authentication and records authentication
// rejections with their reason. These rejections use the unauthenticated
// actor and the system organization.
//
// For authenticated reads, the recorder derives the organizations before
// saving one admission per organization. Organization derivation can read
// membership, metadata, and target records before admission.
//
//   - The recorder saves admissions before invoking the MCP handler.
//   - The recorder saves final read events before writing the response.
//   - Authenticated mutations use the tool and storage recording paths.
func (recorder *RequestRecorder) MCP(authenticate func(http.Handler) http.Handler, handler *Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		operation := mcpOperation(request)
		ctx := auth.WithRejectionNote(request.Context())
		read := newRequestRead(ctx, request, operation)
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

// serveAuthenticated runs handler without recording an HTTP read event when
// the authenticated tool call changes state. Other requests use the verified
// user and the organizations from Handler.requestOrganizations. If
// organization lookup fails, the recorder saves a failed read in the system
// organization and answers with HTTP 503.
func (recorder *RequestRecorder) serveAuthenticated(writer http.ResponseWriter, request *http.Request, read requestRead, handler *Handler) {
	if read.operation.mutation {
		handler.ServeHTTP(writer, request)
		return
	}
	ctx := request.Context()
	userID, _ := auth.UserID(ctx)
	read.actor = userActor(ctx, userID)
	read.selection = read.operation.selection
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

// rejectBeforeAuthentication saves a refused or failed read event before
// writing a response that the server prepared before authentication
// succeeded.
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
