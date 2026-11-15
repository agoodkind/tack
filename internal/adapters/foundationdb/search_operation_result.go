package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// appendSearchOperationResult records the final result of the initiating command
// in the transaction that completes its search work. Autonomous work does not identify an initiator.
func appendSearchOperationResult(
	ctx context.Context,
	tr fdb.Transaction,
	initiation searchdomain.Initiation,
	now time.Time,
	result json.RawMessage,
	details audit.OutcomeDetails,
) error {
	if initiation.OperationID == uuid.Nil && initiation.ActorID == uuid.Nil {
		return nil
	}

	if len(initiation.Operation) == 0 {
		return searchReadFailure(
			ctx,
			"complete search operation "+initiation.OperationID.String(),
			fmt.Errorf(
				"%w: search operation %s lacks its original command event; finish pending search work on the previous release before upgrading",
				audit.ErrMissingMutationContext,
				initiation.OperationID,
			),
		)
	}

	var event audit.Event
	if err := json.Unmarshal(initiation.Operation, &event); err != nil {
		return searchReadFailure(
			ctx,
			"decode search operation "+initiation.OperationID.String(),
			err,
		)
	}

	if event.Context.OperationID != initiation.OperationID || event.Actor.ID != initiation.ActorID {
		return searchReadFailure(
			ctx,
			"complete search operation "+initiation.OperationID.String(),
			fmt.Errorf(
				"search operation %s has conflicting initiating identity",
				initiation.OperationID,
			),
		)
	}

	event.EventID = uuid.NewSHA1(initiation.OperationID, []byte("search-operation-result"))
	event.OccurredAt = now.UTC()
	event.Context.Phase, event.Context.CompletedAt = audit.PhaseResult, event.OccurredAt
	event.Outcome, event.Error = details.Outcome, nil
	event.Context.CommittedSteps = details.CommittedSteps
	if details.Outcome != audit.OutcomeCompleted {
		event.Error = &audit.EventError{
			Code:    details.FailureCode,
			Message: details.FailureMessage,
		}
	}

	extra := make(map[string]json.RawMessage)
	if len(event.Extra) > 0 {
		if err := json.Unmarshal(event.Extra, &extra); err != nil {
			return searchReadFailure(
				ctx,
				"decode search operation evidence "+initiation.OperationID.String(),
				err,
			)
		}
	}

	extra["result"] = result
	var err error
	event.Extra, err = json.Marshal(extra)
	if err != nil {
		return searchReadFailure(
			ctx,
			"encode search operation result "+initiation.OperationID.String(),
			err,
		)
	}

	if err := audit.ValidateEvent(event); err != nil {
		return searchReadFailure(
			ctx,
			"validate search operation result "+initiation.OperationID.String(),
			err,
		)
	}

	encoded, err := audit.EncodeEvent(event, nil)
	if err != nil {
		return searchReadFailure(
			ctx,
			"encode search operation event "+initiation.OperationID.String(),
			err,
		)
	}

	return NewOutboxWriter(tr).WriteEncodedEvent(ctx, encoded)
}

func appendRebuildResult(
	ctx context.Context,
	tr fdb.Transaction,
	rebuild searchdomain.Rebuild,
	now time.Time,
) error {
	details := audit.OutcomeDetails{
		Outcome:        audit.OutcomeCompleted,
		FailureCode:    "",
		FailureMessage: "",
		CommittedSteps: nil,
	}
	if rebuild.State == searchdomain.RebuildFailed {
		details.Outcome = audit.OutcomeFailed
		details.FailureCode, details.FailureMessage = "search_rebuild_failed", rebuild.Failure
	}

	result, err := json.Marshal(
		struct {
			RebuildID   uuid.UUID `json:"rebuild_id"`
			SourceIndex string    `json:"source_index"`
			TargetIndex string    `json:"target_index"`
			State       string    `json:"state"`
		}{
			RebuildID:   rebuild.ID,
			SourceIndex: rebuild.SourceIndex,
			TargetIndex: rebuild.TargetIndex,
			State:       string(details.Outcome),
		},
	)
	if err != nil {
		return searchReadFailure(ctx, "encode rebuild result "+rebuild.ID.String(), err)
	}

	return appendSearchOperationResult(ctx, tr, rebuild.Initiation, now, result, details)
}

func appendRolloutResult(
	ctx context.Context,
	tr fdb.Transaction,
	record searchRolloutRecord,
	now time.Time,
	details audit.OutcomeDetails,
) error {
	result, err := json.Marshal(
		struct {
			AuthorityID     uuid.UUID `json:"authority_id"`
			ActiveVersion   string    `json:"active_version"`
			Generation      int64     `json:"generation"`
			PreviousVersion string    `json:"previous_version,omitempty"`
			State           string    `json:"state"`
		}{
			AuthorityID:     record.AuthorityID,
			ActiveVersion:   record.ActiveVersion,
			Generation:      record.Generation,
			PreviousVersion: record.PreviousVersion,
			State:           string(details.Outcome),
		},
	)
	if err != nil {
		return searchReadFailure(ctx, "encode rollout result "+record.AuthorityID.String(), err)
	}

	return appendSearchOperationResult(ctx, tr, record.rollout().Initiation, now, result, details)
}
