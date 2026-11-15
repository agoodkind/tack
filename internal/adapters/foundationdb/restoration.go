package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/audit"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// restoreRollout records the interrupted rollout and its rollback state.
func (s *SearchAccessRolloutStore) restoreRollout(
	ctx context.Context,
	transaction fdb.Transaction,
	record searchRolloutRecord,
	request searchdomain.RollbackAccessRollout,
	restoredVersion string,
	generation int64,
) (searchRolloutRecord, error) {
	restored := record
	restored.ActiveVersion, restored.CandidateVersion = restoredVersion, restoredVersion
	restored.PreviousVersion, restored.WriteVersions = record.CandidateVersion, []string{
		restoredVersion,
	}
	restored.Phase = string(searchdomain.AccessVerifying)
	restored.ScanCursor, restored.VerifyCursor, restored.RetireCursor = "", "", ""
	restored.ScanComplete, restored.AffectedPass, restored.Generation = false, false, generation
	interrupted := audit.OutcomeDetails{
		Outcome:        audit.OutcomeIncomplete,
		FailureCode:    "access_rollout_rolled_back",
		FailureMessage: "the operator rolled back the access policy rollout",
		CommittedSteps: nil,
	}
	if record.ActiveVersion == record.CandidateVersion {
		interrupted.Outcome = audit.OutcomePartiallyCompleted
		interrupted.CommittedSteps = []string{
			searchdomain.AccessRolloutTargetReference(record.AuthorityID.String()),
		}
	}

	if err := appendRolloutResult(
		ctx,
		transaction,
		restored,
		s.work.clock.Now(),
		interrupted,
	); err != nil {
		return restored, err
	}

	restored.InitiatedOperationID = request.Initiation.OperationID
	restored.InitiatedActorID = request.Initiation.ActorID
	restored.InitiatedOperation = request.Initiation.Operation
	if err := stopRolloutCollecting(
		ctx,
		transaction,
		request.AuthorityID,
		record.Generation,
	); err != nil {
		return restored, err
	}

	if err := writeSearchRecord(
		ctx,
		transaction,
		searchRolloutKey(request.AuthorityID),
		restored,
	); err != nil {
		return restored, err
	}

	return restored, scheduleRolloutWork(ctx, transaction, s.work, request.AuthorityID, generation)
}
