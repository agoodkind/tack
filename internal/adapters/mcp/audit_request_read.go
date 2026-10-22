package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

// recordingUnavailableBody is the response body when the server cannot save a read event.
const recordingUnavailableBody = `{"error":"audit recording unavailable"}`

// requestRead contains the identity and timing of the read event for one request.
type requestRead struct {
	operation   requestOperation
	operationID uuid.UUID
	startedAt   time.Time
	actor       audit.Actor
	orgID       uuid.UUID
	requestID   string
	traceID     string
}

// newRequestRead creates a requestRead with a new operation ID, the unauthenticated actor,
// and the system organization.
func newRequestRead(ctx context.Context, operation requestOperation) requestRead {
	operationID := uuid.Must(uuid.NewV7())
	return requestRead{
		operation: operation, operationID: operationID, startedAt: clock.Now().UTC(),
		actor: unauthenticatedActor(ctx), orgID: audit.SystemOrgID(),
		requestID: telemetry.RequestID(ctx), traceID: telemetry.TraceID(ctx),
	}
}

// admission returns a read admission with the route and operation name as its target.
// The event template does not include request headers, credentials, or the request body.
func (read requestRead) admission() audit.ReadAdmission {
	eventID := uuid.Must(uuid.NewV7())
	operation := read.operation
	name := string(operation.verb)
	target := audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: name, URI: operation.route,
		RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	template := audit.Event{
		Verb: string(operation.verb), EventID: eventID, Actor: read.actor,
		Entity: audit.Entity{Type: audit.TargetKindResource, NodeType: "", ID: uuid.Nil, Identifier: "", Name: name},
		Context: audit.EventContext{
			OrgID: read.orgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: read.requestID, TraceID: read.traceID, Source: operation.source,
			Tool: operation.tool, RPC: operation.method, Reason: "",
			AuditFormatVersion: audit.AuditFormatComplete, OperationClass: audit.OperationRead, Phase: audit.PhaseRead,
			OperationID: read.operationID, StartedAt: read.startedAt, Target: &target,
		},
		Delta: nil, Outcome: "", Error: nil, IdempotencyKey: "", OccurredAt: read.startedAt, Extra: nil,
	}
	token := audit.ReadToken{EventID: eventID, Owner: audit.NewReadOwner(), Generation: 0, AdmissionDeadline: time.Time{}}
	return audit.ReadAdmission{Token: token, Template: template}
}

// serveRead buffers the response from next and saves the final read event before writing the response.
func (recorder *RequestRecorder) serveRead(writer http.ResponseWriter, request *http.Request, read requestRead, next http.Handler) {
	request = request.WithContext(audit.WithReadCollector(request.Context()))
	recorder.release(writer, request, read, func(readRequest *http.Request) audit.ReadResult[*responseCapture] {
		capture := newResponseCapture()
		next.ServeHTTP(capture, readRequest)
		return capture.readResult(readRequest.Context())
	})
}

// release saves a read admission, runs produce, saves the final event, and writes the captured response.
// If ExecuteRead fails or the captured response is nil, release discards any captured response
// and answers with 503.
func (recorder *RequestRecorder) release(
	writer http.ResponseWriter,
	request *http.Request,
	read requestRead,
	produce func(*http.Request) audit.ReadResult[*responseCapture],
) {
	ctx := request.Context()
	var captured *responseCapture
	encoded, err := audit.ExecuteRead(ctx, recorder.store, read.admission(),
		func(readContext context.Context) (audit.ReadResult[*responseCapture], error) {
			result := produce(request.WithContext(readContext))
			captured = result.Value
			return result, nil
		})
	if err != nil || captured == nil {
		recordingUnavailable(ctx, writer, read, err)
		return
	}
	captured.release(writer, encoded.Payload)
}

// recordingUnavailable logs the recording failure and answers with 503.
func recordingUnavailable(ctx context.Context, writer http.ResponseWriter, read requestRead, err error) {
	if err == nil {
		err = fmt.Errorf("read %s did not produce a response", read.operationID)
	}
	telemetry.L(ctx).ErrorContext(ctx, "mcp.audit_request.not_recorded",
		slog.String("operation_id", read.operationID.String()), slog.String("verb", string(read.operation.verb)),
		slog.String("err", err.Error()))
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusServiceUnavailable)
	_, _ = writer.Write([]byte(recordingUnavailableBody))
}

// unauthenticatedActor returns the actor of a request before authentication.
func unauthenticatedActor(ctx context.Context) audit.Actor {
	return audit.Actor{
		Type: audit.ActorUnauthenticated, ID: uuid.Nil, Email: "", Name: "", SessionID: "",
		IP: "", UserAgent: "", RequestID: telemetry.RequestID(ctx), APITokenLabel: "",
	}
}

// userActor returns the verified user actor of an authenticated request.
func userActor(ctx context.Context, userID uuid.UUID) audit.Actor {
	return audit.Actor{
		Type: audit.ActorUser, ID: userID, Email: "", Name: "", SessionID: "",
		IP: "", UserAgent: "", RequestID: telemetry.RequestID(ctx), APITokenLabel: "",
	}
}

// actorOrganization returns the authenticated user's sole organization when ctx provides it.
// It returns the system organization when ctx does not provide exactly one organization.
func actorOrganization(ctx context.Context) uuid.UUID {
	orgIDs, attached := auth.OrgMembership(ctx)
	if attached && len(orgIDs) == 1 {
		return orgIDs[0]
	}
	return audit.SystemOrgID()
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
		return refused(reason, "the request did not include a bearer token")
	case auth.RejectionBearerRefused:
		return refused(reason, "the token store rejected the bearer token")
	case auth.RejectionDevIdentityInvalid:
		return refused(reason, "the development bearer token is not a user ID")
	case auth.RejectionBearerCheckFailed:
		return failed(reason, "the server could not check the bearer token")
	default:
		return refused("authentication_rejected", "the server rejected the request during authentication")
	}
}
