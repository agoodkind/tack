package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// snapshotReferencePrefix starts the external reference of a point in
	// time step.
	snapshotReferencePrefix = "opensearch/point-in-time/"
	// snapshotStepFailedCode and snapshotStepFailedMessage are the fixed
	// failure fields of a point in time step. The event does not store the
	// raw engine error.
	snapshotStepFailedCode       = "point_in_time_failed"
	snapshotStepFailedMessage    = "OpenSearch did not complete the point in time operation"
	snapshotStepUncertainCode    = "point_in_time_uncertain"
	snapshotStepUncertainMessage = "OpenSearch did not confirm the point in time result"
	snapshotStepPartialCode      = "point_in_time_partial"
	snapshotStepPartialMessage   = "OpenSearch created a partial point in time"
	// snapshotWriteTimeout limits the result write of a point in time step.
	// It equals the session transaction limit.
	snapshotWriteTimeout = 5 * time.Second
)

// snapshotStepError is a failed point in time effect. It keeps the text of
// err. queryFailure and the callers of closeSnapshot classify and log the
// failure once, and an expected refusal stays below the error level.
type snapshotStepError struct {
	err error
}

func (e snapshotStepError) Error() string { return e.err.Error() }
func (e snapshotStepError) Unwrap() error { return e.err }

// snapshotOperation is one point in time operation. Its action ends the
// external reference, because one request can create and delete the same
// point in time and each step needs its own intent event ID.
type snapshotOperation struct {
	verb   audit.Verb
	action string
}

var (
	snapshotCreate = snapshotOperation{verb: audit.VerbSearchPointInTimeCreate, action: "create"}
	snapshotDelete = snapshotOperation{verb: audit.VerbSearchPointInTimeDelete, action: "delete"}
)

// snapshotEvidence is the planned and observed evidence of a point in time
// step. The planned evidence of a create step omits the point in time ID.
type snapshotEvidence struct {
	SessionID uuid.UUID `json:"session_id"`
	Index     string    `json:"index"`
	PITID     string    `json:"pit_id,omitempty"`
}

// closedSnapshotEvidence is the observed evidence of a delete step.
// AlreadyClosed reports an explicit missing point in time response from
// OpenSearch. An idempotent success does not establish prior existence.
type closedSnapshotEvidence struct {
	snapshotEvidence
	AlreadyClosed bool `json:"already_closed"`
}

// runSnapshotStep records the operation intent and planned step evidence
// before running effect. It then records observed evidence and the result.
// An intent write failure prevents the effect.
func (s *SearchQueryService) runSnapshotStep(
	ctx context.Context,
	session searchdomain.Session,
	operation snapshotOperation,
	planned snapshotEvidence,
	effect func(context.Context) (snapshotEvidence, error),
) error {
	return s.runEncodedStep(ctx, session, operation, planned, func(ctx context.Context) (json.RawMessage, error) {
		observed, effectErr := effect(ctx)
		return encodeEvidence(ctx, observed, effectErr)
	})
}

func (s *SearchQueryService) runEncodedStep(
	ctx context.Context,
	session searchdomain.Session,
	operation snapshotOperation,
	planned snapshotEvidence,
	effect func(context.Context) (json.RawMessage, error),
) error {
	mutation, err := audit.RequireMutationContext(ctx)
	if err != nil {
		return snapshotStepFailure(ctx, "read the audit operation", err)
	}
	encoded, err := encodeEvidence(ctx, planned, nil)
	if err != nil {
		return err
	}
	mutation.Context.WorkspaceID, mutation.Context.ScopeID, mutation.Context.ParentID = session.EntryPointID, uuid.Nil, uuid.Nil
	intent := audit.ExternalIntent(mutation, operation.verb, audit.SearchSessionTarget(session.ID, session.PrincipalID), session.AuthorityID)
	target := audit.Target{
		Kind: audit.TargetKindExternal, NodeType: "", Name: "", URI: "", RelationType: "",
		Reference: snapshotReferencePrefix + session.ID.String() + "/" + operation.action,
		ID:        uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	err = audit.RunExternal(ctx, s.ports.Audit, intent, func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
		step := audit.ExternalStep(target, encoded)
		details := audit.OutcomeDetails{
			Outcome: audit.OutcomeFailed, FailureCode: snapshotStepFailedCode, FailureMessage: snapshotStepFailedMessage, CommittedSteps: nil,
		}
		if beginErr := steps.BeginStep(ctx, step); beginErr != nil {
			return details, snapshotStepFailure(ctx, "record the point in time step", beginErr)
		}
		observed, effectErr := effect(ctx)
		switch {
		case effectErr == nil:
			details = audit.OutcomeDetails{Outcome: audit.OutcomeCompleted, FailureCode: "", FailureMessage: "", CommittedSteps: nil}
		case errors.Is(effectErr, searchdomain.ErrSnapshotUncertain):
			details = audit.OutcomeDetails{Outcome: audit.OutcomeIncomplete, FailureCode: snapshotStepUncertainCode, FailureMessage: snapshotStepUncertainMessage, CommittedSteps: nil}
		case errors.Is(effectErr, searchdomain.ErrSnapshotPartial):
			details = audit.OutcomeDetails{Outcome: audit.OutcomePartiallyCompleted, FailureCode: snapshotStepPartialCode, FailureMessage: snapshotStepPartialMessage, CommittedSteps: []string{target.Reference}}
		}
		writeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), snapshotWriteTimeout)
		defer cancel()
		if finishErr := steps.FinishStep(writeContext, audit.FinishedStep(step, details, observed)); finishErr != nil {
			slog.ErrorContext(ctx, "search.snapshot.step_failed", slog.String("err", finishErr.Error()))
			return details, errors.Join(effectErr, fmt.Errorf("record the point in time step result: %w", finishErr))
		}
		return details, effectErr
	})
	if err != nil {
		return snapshotStepError{err: err}
	}
	return nil
}

func (s *SearchQueryService) closeSnapshot(ctx context.Context, session searchdomain.Session, snapshot searchdomain.Snapshot) error {
	if snapshot.PITID == "" {
		return nil
	}
	planned := snapshotEvidence{SessionID: session.ID, Index: snapshot.Index, PITID: snapshot.PITID}
	return s.runEncodedStep(ctx, session, snapshotDelete, planned, func(ctx context.Context) (json.RawMessage, error) {
		closeErr := s.ports.Ranker.Close(ctx, snapshot)
		alreadyClosed := errors.Is(closeErr, searchdomain.ErrSnapshotLost)
		if closeErr != nil && !alreadyClosed {
			return nil, snapshotStepError{err: closeErr}
		}
		return encodeEvidence(ctx, closedSnapshotEvidence{snapshotEvidence: planned, AlreadyClosed: alreadyClosed}, nil)
	})
}

// recordCleanupFailure records one failed cleanup step. If the audit save
// fails, it logs the failure and returns an error containing
// searchdomain.ErrEngineUnavailable, audit.ErrEventNotRecorded, the cleanup
// error, and the save error.
func (s *SearchQueryService) recordCleanupFailure(ctx context.Context, sessionID uuid.UUID, step searchdomain.CleanupStep, cause error) error {
	if err := s.ports.Sessions.RecordCleanupFailure(ctx, sessionID, step, cause); err != nil {
		wrapped := errors.Join(searchdomain.ErrEngineUnavailable, audit.ErrEventNotRecorded,
			fmt.Errorf("search session %s cleanup step %s failed: %w", sessionID, step, cause),
			fmt.Errorf("record the failed cleanup step %s of search session %s: %w", step, sessionID, err))
		telemetry.L(ctx).ErrorContext(ctx, "search.session.audit_failed", slog.String("err", wrapped.Error()), slog.String("session_id", sessionID.String()))
		return wrapped
	}
	return nil
}

type snapshotEvidenceValue interface {
	snapshotEvidence | closedSnapshotEvidence
}

func encodeEvidence[Evidence snapshotEvidenceValue](ctx context.Context, evidence Evidence, effectErr error) (json.RawMessage, error) {
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return nil, snapshotStepFailure(ctx, "encode the point in time evidence", errors.Join(effectErr, err))
	}
	return encoded, effectErr
}

func snapshotStepFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("search snapshot step: %s: %w", operation, err)
	slog.ErrorContext(ctx, "search.snapshot.step_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
