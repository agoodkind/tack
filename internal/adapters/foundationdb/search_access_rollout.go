package foundationdb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/telemetry"
)

// SearchAccessRolloutStore persists access policy rollouts in FoundationDB.
// Each rollout step runs under a claimed rollout work item.
type SearchAccessRolloutStore struct {
	db       fdb.Database
	work     *SearchWorkStore
	policies *searchaccess.PolicySet
}

var (
	_ searchdomain.AccessRolloutStore = (*SearchAccessRolloutStore)(nil)
	_ searchdomain.AccessStateReader  = (*SearchAccessRolloutStore)(nil)
)

// NewSearchAccessRolloutStore creates the rollout store. work verifies
// claims and supplies the injected clock.
func NewSearchAccessRolloutStore(db fdb.Database, work *SearchWorkStore, policies *searchaccess.PolicySet) *SearchAccessRolloutStore {
	return &SearchAccessRolloutStore{db: db, work: work, policies: policies}
}

// Current returns the rollout state of authorityID.
func (s *SearchAccessRolloutStore) Current(ctx context.Context, authorityID uuid.UUID) (rollout searchdomain.AccessRollout, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rollout.current")(&err)
	var record searchRolloutRecord
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		var readErr error
		record, readErr = readRollout(ctx, tr, authorityID)
		return readErr
	})
	if err != nil {
		return rollout, searchStorageError(ctx, "search.rollout.read_failed", "read access rollout of authority "+authorityID.String(), uuid.Nil, err)
	}
	return record.rollout(), nil
}

// ActiveAccessVersion returns the version that serves new sessions of
// authorityID.
func (s *SearchAccessRolloutStore) ActiveAccessVersion(ctx context.Context, authorityID uuid.UUID) (string, error) {
	rollout, err := s.Current(ctx, authorityID)
	if err != nil {
		return "", err
	}
	return rollout.ActiveVersion, nil
}

// Begin starts one candidate version for a stable authority. In one
// transaction it records both write versions, the permission event
// boundary, and the backfill phase, and schedules the rollout work item.
func (s *SearchAccessRolloutStore) Begin(ctx context.Context, request searchdomain.BeginAccessRollout) (rollout searchdomain.AccessRollout, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rollout.begin")(&err)
	if request.AuthorityID == uuid.Nil || !s.policies.Supports(request.CandidateVersion) {
		invalid := fmt.Errorf("candidate version %q is not a registered policy, or the authority is empty", request.CandidateVersion)
		return rollout, searchStorageError(ctx, "search.rollout.begin_invalid", "begin access rollout", uuid.Nil, invalid)
	}
	var begun searchRolloutRecord
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		record, readErr := readRollout(ctx, tr, request.AuthorityID)
		if readErr != nil {
			return readErr
		}
		if record.Phase != string(searchdomain.AccessStable) || record.Generation != request.ExpectedGeneration ||
			record.ActiveVersion == request.CandidateVersion {
			return searchdomain.ErrRolloutInProgress
		}
		generation, counterErr := incrementSearchCounter(ctx, tr, searchRolloutGenerationKey(request.AuthorityID))
		if counterErr != nil {
			return counterErr
		}
		event, eventErr := readPermissionEvent(ctx, tr, request.AuthorityID)
		if eventErr != nil {
			return eventErr
		}
		versions := []string{record.ActiveVersion, request.CandidateVersion}
		slices.Sort(versions)
		begun = record
		begun.CandidateVersion, begun.WriteVersions = request.CandidateVersion, versions
		begun.Phase, begun.ScanCursor, begun.VerifyCursor, begun.RetireCursor = string(searchdomain.AccessBackfill), "", "", ""
		begun.ScanComplete, begun.Generation, begun.PermissionEventVersion = false, generation, event
		if writeErr := writeSearchRecord(ctx, tr, searchRolloutKey(request.AuthorityID), begun); writeErr != nil {
			return writeErr
		}
		return scheduleRolloutWork(ctx, tr, s.work, request.AuthorityID, generation)
	})
	if errors.Is(err, searchdomain.ErrRolloutInProgress) {
		return rollout, err
	}
	if err != nil {
		return rollout, searchStorageError(ctx, "search.rollout.begin_failed", "begin access rollout of authority "+request.AuthorityID.String(), uuid.Nil, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.rollout.begun", slog.String("authority_id", request.AuthorityID.String()),
		slog.String("candidate_version", request.CandidateVersion), slog.Int64("generation", begun.Generation))
	return begun.rollout(), nil
}

// scheduleRolloutWork writes the rollout work item of authorityID at
// generation.
func scheduleRolloutWork(ctx context.Context, tr fdb.Transaction, work *SearchWorkStore, authorityID uuid.UUID, generation int64) error {
	class := searchdomain.WorkClassRollout
	existing, found, err := readSearchWork(ctx, tr, class, authorityID, uuid.Nil)
	if err != nil {
		return err
	}
	var previous *searchWorkRecord
	if found {
		previous = &existing
	}
	record := searchWorkRecord{
		OrgID: authorityID, NodeID: uuid.Nil, Generation: generation, Revision: 0,
		Deleted: false, EnqueuedAt: work.clock.Now().UTC(),
	}
	return writeSearchWork(ctx, tr, class, record, previous)
}

// Wait releases the rollout claim for the retry delay without recording a
// failure. The rollout waits for pending page work or open sessions.
func (s *SearchAccessRolloutStore) Wait(ctx context.Context, work searchdomain.Work) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_rollout.wait")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		if _, verifyErr := s.work.verifyClaim(ctx, tr, work); verifyErr != nil {
			return verifyErr
		}
		return writeSearchRecord(ctx, tr, searchClaimKey(string(work.Class), searchBucket(work.OrgID, work.NodeID), work.OrgID, work.NodeID), searchClaimRecord{
			Owner: "", Generation: work.Generation, LeaseUntil: s.work.clock.Now().Add(searchRetryDelay), Target: work.Target,
		})
	})
	if err != nil {
		return searchStorageError(ctx, "search.rollout.wait_failed", "delay access rollout of authority "+work.OrgID.String(), uuid.Nil, err)
	}
	return nil
}
