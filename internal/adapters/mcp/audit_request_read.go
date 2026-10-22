package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

// recordingUnavailableBody is the response body when the server cannot save a read event.
const recordingUnavailableBody = `{"error":"audit recording unavailable"}`

// requestRead contains the identity and timing of the read events for one request.
// The recorder saves one event for each organization in orgIDs.
type requestRead struct {
	operation   requestOperation
	operationID uuid.UUID
	startedAt   time.Time
	actor       audit.Actor
	orgIDs      []uuid.UUID
	requestID   string
	traceID     string
}

// newRequestRead creates a requestRead with a new operation ID, the unauthenticated actor,
// and the system organization.
func newRequestRead(ctx context.Context, operation requestOperation) requestRead {
	operationID := uuid.Must(uuid.NewV7())
	return requestRead{
		operation: operation, operationID: operationID, startedAt: clock.Now().UTC(),
		actor: unauthenticatedActor(ctx), orgIDs: []uuid.UUID{audit.SystemOrgID()},
		requestID: telemetry.RequestID(ctx), traceID: telemetry.TraceID(ctx),
	}
}

// admissions returns one read admission for each organization of the read.
// Each admission uses the same operation ID, actor, and start time.
// Each target identifies the route and operation. The event templates do not include
// request headers, credentials, or the request body.
func (read requestRead) admissions() []audit.ReadAdmission {
	operation := read.operation
	name := string(operation.verb)
	admissions := make([]audit.ReadAdmission, 0, len(read.orgIDs))
	for _, orgID := range read.orgIDs {
		eventID := uuid.Must(uuid.NewV7())
		target := audit.Target{
			Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: name, URI: operation.route,
			RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
		}
		template := audit.Event{
			Verb: string(operation.verb), EventID: eventID, Actor: read.actor,
			Entity: audit.Entity{Type: audit.TargetKindResource, NodeType: "", ID: uuid.Nil, Identifier: "", Name: name},
			Context: audit.EventContext{
				OrgID: orgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
				RequestID: read.requestID, TraceID: read.traceID, Source: operation.source,
				Tool: operation.tool, RPC: operation.method, Reason: "",
				AuditFormatVersion: audit.AuditFormatComplete, OperationClass: audit.OperationRead, Phase: audit.PhaseRead,
				OperationID: read.operationID, StartedAt: read.startedAt, Target: &target,
			},
			Delta: nil, Outcome: "", Error: nil, IdempotencyKey: "", OccurredAt: read.startedAt, Extra: nil,
		}
		token := audit.ReadToken{EventID: eventID, Owner: audit.NewReadOwner(), Generation: 0, AdmissionDeadline: time.Time{}}
		admissions = append(admissions, audit.ReadAdmission{Token: token, Template: template})
	}
	return admissions
}

// serveRead buffers the response from next and saves the final read events before writing the response.
func (recorder *RequestRecorder) serveRead(writer http.ResponseWriter, request *http.Request, read requestRead, next http.Handler) {
	request = request.WithContext(audit.WithReadCollector(request.Context()))
	recorder.release(writer, request, read, func(readRequest *http.Request) audit.ReadResult[*responseCapture] {
		capture := newResponseCapture()
		next.ServeHTTP(capture, readRequest)
		return capture.readResult(readRequest.Context())
	})
}

// release saves the read admissions, runs produce, saves the final events, and writes the captured response.
// For one organization, it records every returned target in one event.
// For several organizations, it records each organization's targets in a separate event.
// If saving an admission or final event fails, or the captured response is nil,
// release discards the captured response and returns 503.
func (recorder *RequestRecorder) release(
	writer http.ResponseWriter,
	request *http.Request,
	read requestRead,
	produce func(*http.Request) audit.ReadResult[*responseCapture],
) {
	ctx := request.Context()
	var captured *responseCapture
	run := func(readContext context.Context) (audit.ReadResult[*responseCapture], error) {
		result := produce(request.WithContext(readContext))
		captured = result.Value
		return result, nil
	}
	admissions := read.admissions()
	var encoded audit.EncodedRead
	var err error
	if len(admissions) == 1 {
		encoded, err = audit.ExecuteRead(ctx, recorder.store, admissions[0], run)
	} else {
		encoded, err = audit.ExecuteReadGroup(ctx, recorder.store, admissions, run)
	}
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
