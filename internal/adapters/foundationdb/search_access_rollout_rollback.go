package foundationdb

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// Rollback restores the version other than the candidate in one FoundationDB
// transaction. It requires a rollout in backfill or verifying that still
// writes both versions. It sets the restored version as active and
// candidate, records the old candidate as previous, and selects only the
// restored version for writes. It enters verifying, clears the cursors,
// clears the collection key and the old generation's affected set and
// counter, increments the generation, and schedules rollout work at that
// generation. Worker steps from the old generation fail their claim check. A
// repeated rollback returns the stored state without writes when the stored
// rollout is verifying, uses the restored version as both active and
// candidate, writes only that version, records a previous version, and has a
// generation equal to ExpectedGeneration or ExpectedGeneration plus one.
// Otherwise, it returns [searchdomain.ErrNoRolloutInProgress] for a stable
// authority. It returns [searchdomain.ErrRetirementStarted] if retirement
// has started or the write versions do not contain the candidate and exactly
// one other version. It returns [searchdomain.ErrRolloutChanged] if the
// stored generation differs from ExpectedGeneration.
func (s *SearchAccessRolloutStore) Rollback(ctx context.Context, request searchdomain.RollbackAccessRollout) (rollout searchdomain.AccessRollout, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rollout.rollback")(&err)
	if request.AuthorityID == uuid.Nil {
		return rollout, searchStorageError(ctx, "search.rollout.rollback_invalid", "roll back access rollout", uuid.Nil, errors.New("authority ID is empty"))
	}
	var restored searchRolloutRecord
	repeated := false
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		record, readErr := readRollout(ctx, tr, request.AuthorityID)
		if readErr != nil {
			return readErr
		}
		repeated = rolledBack(record, request.ExpectedGeneration)
		if repeated {
			restored = record
			return nil
		}
		restoredVersion, refusal := rollbackTarget(record)
		if refusal != nil {
			return refusal
		}
		if record.Generation != request.ExpectedGeneration {
			return searchdomain.ErrRolloutChanged
		}
		generation, counterErr := incrementSearchCounter(ctx, tr, searchRolloutGenerationKey(request.AuthorityID))
		if counterErr != nil {
			return counterErr
		}
		restored = record
		restored.ActiveVersion, restored.CandidateVersion = restoredVersion, restoredVersion
		restored.PreviousVersion, restored.WriteVersions = record.CandidateVersion, []string{restoredVersion}
		restored.Phase, restored.ScanCursor, restored.VerifyCursor, restored.RetireCursor = string(searchdomain.AccessVerifying), "", "", ""
		restored.ScanComplete, restored.AffectedPass, restored.Generation = false, false, generation
		restored.InitiatedOperationID, restored.InitiatedActorID = request.Initiation.OperationID, request.Initiation.ActorID
		if stopErr := stopRolloutCollecting(ctx, tr, request.AuthorityID, record.Generation); stopErr != nil {
			return stopErr
		}
		if writeErr := writeSearchRecord(ctx, tr, searchRolloutKey(request.AuthorityID), restored); writeErr != nil {
			return writeErr
		}
		return scheduleRolloutWork(ctx, tr, s.work, request.AuthorityID, generation)
	})
	if errors.Is(err, searchdomain.ErrNoRolloutInProgress) || errors.Is(err, searchdomain.ErrRetirementStarted) ||
		errors.Is(err, searchdomain.ErrRolloutChanged) {
		return rollout, err
	}
	if err != nil {
		return rollout, searchStorageError(ctx, "search.rollout.rollback_failed", "roll back access rollout of authority "+request.AuthorityID.String(), uuid.Nil, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.rollout.rolled_back", slog.String("authority_id", request.AuthorityID.String()),
		slog.String("active_version", restored.ActiveVersion), slog.String("removed_version", restored.PreviousVersion),
		slog.Int64("generation", restored.Generation), slog.Bool("repeated", repeated))
	return restored.rollout(), nil
}

// rollbackTarget returns the version rollback makes active or the refusal
// error when the record cannot roll back.
func rollbackTarget(record searchRolloutRecord) (string, error) {
	if record.Phase == string(searchdomain.AccessStable) {
		return "", searchdomain.ErrNoRolloutInProgress
	}
	if record.Phase == string(searchdomain.AccessRetiring) || len(record.WriteVersions) != 2 ||
		!slices.Contains(record.WriteVersions, record.CandidateVersion) {
		return "", searchdomain.ErrRetirementStarted
	}
	for _, version := range record.WriteVersions {
		if version != record.CandidateVersion {
			return version, nil
		}
	}
	return "", searchdomain.ErrRetirementStarted
}

// rolledBack reports whether the stored rollout already contains the result
// of the rollback requested at expectedGeneration.
func rolledBack(record searchRolloutRecord, expectedGeneration int64) bool {
	restoredState := record.Phase == string(searchdomain.AccessVerifying) && record.ActiveVersion == record.CandidateVersion &&
		record.PreviousVersion != "" && slices.Equal(record.WriteVersions, []string{record.ActiveVersion})
	return restoredState && (record.Generation == expectedGeneration || record.Generation == expectedGeneration+1)
}
