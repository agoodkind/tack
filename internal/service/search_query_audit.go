package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

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
	snapshotStepFailedCode    = "point_in_time_failed"
	snapshotStepFailedMessage = "OpenSearch did not complete the point in time operation"
)

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
// AlreadyClosed reports that OpenSearch no longer recognized the point in
// time.
type closedSnapshotEvidence struct {
	snapshotEvidence
	AlreadyClosed bool `json:"already_closed"`
}

// runSnapshotStep saves the intent of operation on the session target and one
// external step with planned, then runs effect and saves the observed
// evidence and result. effect does not run when an intent cannot be saved.
func (s *SearchQueryService) runSnapshotStep(
	ctx context.Context,
	session searchdomain.Session,
	operation snapshotOperation,
	planned snapshotEvidence,
	effect func(context.Context) (snapshotEvidence, error),
) error {
	return s.runEncodedStep(ctx, session, operation, planned, func(ctx context.Context) (json.RawMessage, error) {
		observed, err := effect(ctx)
		if err != nil {
			return nil, err
		}
		return encodeEvidence(ctx, observed)
	})
}

// runEncodedStep is runSnapshotStep with an effect that returns encoded
// evidence.
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
	encoded, err := encodeEvidence(ctx, planned)
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
		if effectErr == nil {
			details = audit.OutcomeDetails{Outcome: audit.OutcomeCompleted, FailureCode: "", FailureMessage: "", CommittedSteps: nil}
		}
		if finishErr := steps.FinishStep(ctx, audit.FinishedStep(step, details, observed)); finishErr != nil {
			slog.ErrorContext(ctx, "search.snapshot.step_failed", slog.String("err", finishErr.Error()))
			return details, errors.Join(effectErr, fmt.Errorf("record the point in time step result: %w", finishErr))
		}
		return details, effectErr
	})
	if err != nil {
		return snapshotStepFailure(ctx, "run the point in time step", err)
	}
	return nil
}

// closeSnapshot deletes snapshot under one audited step. It records a point
// in time that OpenSearch no longer recognizes as a completed step with
// already_closed set, and it returns nil for that point in time. A snapshot
// without an ID does not have a point in time to delete.
func (s *SearchQueryService) closeSnapshot(ctx context.Context, session searchdomain.Session, snapshot searchdomain.Snapshot) error {
	if snapshot.PITID == "" {
		return nil
	}
	planned := snapshotEvidence{SessionID: session.ID, Index: snapshot.Index, PITID: snapshot.PITID}
	return s.runEncodedStep(ctx, session, snapshotDelete, planned, func(ctx context.Context) (json.RawMessage, error) {
		closeErr := s.ports.Ranker.Close(ctx, snapshot)
		alreadyClosed := errors.Is(closeErr, searchdomain.ErrSnapshotLost)
		if closeErr != nil && !alreadyClosed {
			return nil, snapshotStepFailure(ctx, "delete the point in time", closeErr)
		}
		return encodeEvidence(ctx, closedSnapshotEvidence{snapshotEvidence: planned, AlreadyClosed: alreadyClosed})
	})
}

// recordCleanupFailure records one failed cleanup step and logs a failure to
// record it. The search result does not depend on the cleanup.
func (s *SearchQueryService) recordCleanupFailure(ctx context.Context, sessionID uuid.UUID, step searchdomain.CleanupStep, cause error) {
	if err := s.ports.Sessions.RecordCleanupFailure(ctx, sessionID, step, cause); err != nil {
		telemetry.L(ctx).ErrorContext(ctx, "search.session.audit_failed", slog.String("err", err.Error()), slog.String("session_id", sessionID.String()))
	}
}

type snapshotEvidenceValue interface {
	snapshotEvidence | closedSnapshotEvidence
}

func encodeEvidence[Evidence snapshotEvidenceValue](ctx context.Context, evidence Evidence) (json.RawMessage, error) {
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return nil, snapshotStepFailure(ctx, "encode the point in time evidence", err)
	}
	return encoded, nil
}

func snapshotStepFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("search snapshot step: %s: %w", operation, err)
	slog.ErrorContext(ctx, "search.snapshot.step_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
