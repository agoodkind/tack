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

// Rollback restores the version other than the candidate in one
// FoundationDB transaction. It requires a rollout in backfill or
// verifying that still writes both versions. It records the restored
// version as active and candidate and the old candidate as previous.
// It sets the write versions to the restored version only, enters
// verifying, clears the cursors, increments the rollout generation, and
// schedules rollout work at that generation. Worker steps from the old
// generation fail their claim check. It returns
// [searchdomain.ErrNoRolloutInProgress] for a stable authority and
// [searchdomain.ErrRetirementStarted] when retirement has started or the
// authority writes only one version. It returns
// [searchdomain.ErrRolloutChanged] when the stored generation differs
// from ExpectedGeneration.
func (s *SearchAccessRolloutStore) Rollback(ctx context.Context, request searchdomain.RollbackAccessRollout) (rollout searchdomain.AccessRollout, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rollout.rollback")(&err)
	if request.AuthorityID == uuid.Nil {
		return rollout, searchStorageError(ctx, "search.rollout.rollback_invalid", "roll back access rollout", uuid.Nil, errors.New("authority ID is empty"))
	}
	var restored searchRolloutRecord
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		record, readErr := readRollout(ctx, tr, request.AuthorityID)
		if readErr != nil {
			return readErr
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
		if clearErr := clearRolloutAffected(ctx, tr, request.AuthorityID, record.Generation); clearErr != nil {
			return clearErr
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
		slog.Int64("generation", restored.Generation))
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
