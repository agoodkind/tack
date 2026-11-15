// Package mcp serves the MCP endpoint and records HTTP read and rejection events.
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

// RequestRecorder records access attempts and security decisions.
// FoundationDB writers record changes from authenticated MCP tools that change state.
type RequestRecorder struct {
	store audit.BatchOutboxWriter
}

// NewRequestRecorder returns a recorder that saves read events in store.
func NewRequestRecorder(store audit.BatchOutboxWriter) *RequestRecorder {
	return &RequestRecorder{store: store}
}

// Health returns 405 for methods other than GET or HEAD.
func (recorder *RequestRecorder) Health(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			methodNotAllowed(
				writer,
				http.MethodGet+", "+http.MethodHead,
				healthMethodNotAllowedBody,
			)

			return
		}

		next.ServeHTTP(writer, request)
	})
}

// UnknownRoute records each request to a route that the server does not
// register and answers with 404.
func (recorder *RequestRecorder) UnknownRoute() http.Handler {
	return recorder.route(
		routeOperation(audit.VerbHTTPRouteUnknown, unknownRoute),
		http.NotFoundHandler(),
	)
}

// route records each request to a route that does not authenticate. The event
// uses the unauthenticated actor and the system organization.
func (recorder *RequestRecorder) route(operation requestOperation, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorder.serveRead(
			writer,
			request,
			newRequestRead(request.Context(), request, operation),
			next,
		)
	})
}

// MCP rejects GET requests before authentication and records authentication rejections with their reason.
// It uses the unauthenticated actor and the system organization for these rejections.
// For an authenticated request that does not change state, it derives the organizations
// and saves one admission per organization before the handler reads data.
func (recorder *RequestRecorder) MCP(
	authenticate func(http.Handler) http.Handler,
	handler *Handler,
) http.Handler {
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
		authenticated := http.HandlerFunc(
			func(_ http.ResponseWriter, authenticatedRequest *http.Request) {
				handled = true
				recorder.serveAuthenticated(writer, authenticatedRequest, read, handler)
			},
		)
		capture := newResponseCapture()
		authenticate(authenticated).ServeHTTP(capture, request)
		if handled {
			return
		}

		recorder.rejectBeforeAuthentication(writer, request, read, capture)
	})
}

// serveAuthenticated records the verified access attempt before the tool runs.
func (recorder *RequestRecorder) serveAuthenticated(
	writer http.ResponseWriter,
	request *http.Request,
	read requestRead,
	handler *Handler,
) {
	if read.operation.verb == audit.VerbMCPProtocolRead {
		handler.ServeHTTP(writer, request)
		return
	}

	ctx := request.Context()
	userID, _ := auth.UserID(ctx)
	read.actor = userActor(ctx, userID)
	orgIDs, err := handler.requestOrganizations(ctx, &read.operation)
	read.selection = read.operation.selection
	if err != nil {
		capture := organizationUnavailable()
		recorder.respond(writer, request, read, capture, audit.NoOutcome())

		return
	}

	read.orgIDs = orgIDs
	if read.operation.mutation {
		if recorder.record(writer, request, read, audit.NoOutcome()) {
			handler.ServeHTTP(writer, request)
		}

		return
	}

	recorder.serveRead(writer, request, read, handler)
}

// rejectBeforeAuthentication saves a refused or failed read event before writing a response
// that the server prepared before authentication succeeded.
func (recorder *RequestRecorder) rejectBeforeAuthentication(
	writer http.ResponseWriter,
	request *http.Request,
	read requestRead,
	capture *responseCapture,
) {
	reason, rejected := auth.RejectionReason(request.Context())
	if rejected {
		read.operation.verb = rejectionVerb(reason)
	}

	decision := statusOutcome(capture.status)
	if rejected {
		decision = rejectionOutcome(reason)
	}

	recorder.respond(writer, request, read, capture, decision)
}
