package foundationdb

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

const searchClaimScanLimit = 32

func (s *SearchWorkStore) claimBucket(
	ctx context.Context,
	class searchdomain.WorkClass,
	bucket int,
	owner string,
	lease time.Duration,
) (searchdomain.Work, error) {
	var selected searchdomain.Work
	claimed := false
	// A bucket without claimable work returns nil from the closure. The
	// transaction then commits the clears of stale age keys.
	err := transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		target, err := tr.Get(fdb.Key(searchIndexKey())).Get()
		if err != nil {
			return searchReadFailure(ctx, "read serving search index", err)
		}
		if len(target) == 0 {
			return searchdomain.ErrNoServingIndex
		}
		keyRange, err := fdb.PrefixRange(searchAgePrefix(string(class), bucket))
		if err != nil {
			return searchReadFailure(ctx, "create search age range for bucket "+strconv.Itoa(bucket), err)
		}
		items, err := tr.GetRange(keyRange, fdb.RangeOptions{Limit: searchClaimScanLimit}).GetSliceWithError()
		if err != nil {
			return searchReadFailure(ctx, "read search age bucket "+strconv.Itoa(bucket), err)
		}
		selected, claimed, err = s.claimFirstItem(ctx, tr, class, bucket, owner, lease, string(target), items)
		return err
	})
	var none searchdomain.Work
	if err != nil {
		if errors.Is(err, searchdomain.ErrNoServingIndex) {
			return none, err
		}
		return none, searchStorageError(ctx, "search.work.claim_failed", "claim search work in bucket "+strconv.Itoa(bucket), uuid.Nil, err)
	}
	if !claimed {
		return none, searchdomain.ErrNoWork
	}
	return selected, nil
}

func (s *SearchWorkStore) claimWorkItem(
	ctx context.Context,
	tr fdb.Transaction,
	class searchdomain.WorkClass,
	owner string,
	lease time.Duration,
	target string,
	record searchWorkRecord,
) (searchdomain.Work, bool, error) {
	var none searchdomain.Work
	bucket := searchBucket(record.OrgID, record.NodeID)
	claimKey := searchClaimKey(string(class), bucket, record.OrgID, record.NodeID)
	desired, err := readSearchCounter(ctx, tr, desiredGenerationKey(class, record.OrgID, record.NodeID))
	if err != nil {
		return none, false, err
	}
	if desired != record.Generation {
		removeSearchWork(tr, class, record)
		tr.Clear(fdb.Key(claimKey))
		return none, false, nil
	}
	var claim searchClaimRecord
	found, err := readSearchRecord(ctx, tr, claimKey, &claim)
	if err != nil {
		return none, false, err
	}
	now := s.clock.Now()
	if found && claim.Generation == record.Generation && claim.Target == target && claim.LeaseUntil.After(now) {
		return none, false, nil
	}
	claim = searchClaimRecord{Owner: owner, Generation: record.Generation, LeaseUntil: now.Add(lease), Target: target}
	if err := writeSearchRecord(ctx, tr, claimKey, claim); err != nil {
		return none, false, err
	}
	progress, err := s.readProgress(ctx, tr, class, record)
	if err != nil {
		return none, false, err
	}
	return searchdomain.Work{
		OrgID: record.OrgID, NodeID: record.NodeID, Generation: record.Generation,
		Revision: strconv.FormatInt(record.Revision, 10), Projection: progress.Projection,
		Cursor: progress.Cursor, Ordinal: progress.Ordinal, Phase: searchdomain.WorkPhase(progress.Phase),
		Deleted: record.Deleted, EnqueuedAt: record.EnqueuedAt, Owner: owner,
		LeaseUntil: claim.LeaseUntil, Class: class, Target: target,
	}, true, nil
}

// readProgress returns the checkpoint that belongs to record. A checkpoint
// from another revision or generation reads as the first page. A rescan
// reads its scan state, which survives newer rescan requests.
func (s *SearchWorkStore) readProgress(ctx context.Context, tr fdb.Transaction, class searchdomain.WorkClass, record searchWorkRecord) (searchProgressRecord, error) {
	initial := searchProgressRecord{
		Revision: record.Revision, Generation: record.Generation, Projection: "",
		Cursor: "", Ordinal: 0, Phase: string(searchdomain.PhasePages),
	}
	if class == searchdomain.WorkClassRescan {
		return rescanProgress(ctx, tr, record, initial)
	}
	var progress searchProgressRecord
	found, err := readSearchRecord(ctx, tr, searchCursorKey(string(class), record.OrgID, record.NodeID), &progress)
	if err != nil || !found {
		return initial, err
	}
	if class == searchdomain.WorkClassLive && progress.Revision != record.Revision {
		return initial, nil
	}
	if class != searchdomain.WorkClassLive && progress.Generation != record.Generation {
		return initial, nil
	}
	return progress, nil
}

// verifyClaim requires the pending record, desired generation, lease, and
// serving index to match work. It returns the pending record.
func (s *SearchWorkStore) verifyClaim(ctx context.Context, tr fdb.Transaction, work searchdomain.Work) (searchWorkRecord, error) {
	bucket := searchBucket(work.OrgID, work.NodeID)
	var record searchWorkRecord
	found, err := readSearchRecord(ctx, tr, searchWorkKey(string(work.Class), bucket, work.OrgID, work.NodeID), &record)
	if err != nil || !found || record.Generation != work.Generation {
		return record, claimMismatch(err)
	}
	desired, err := readSearchCounter(ctx, tr, desiredGenerationKey(work.Class, work.OrgID, work.NodeID))
	if err != nil || desired != work.Generation {
		return record, claimMismatch(err)
	}
	var claim searchClaimRecord
	found, err = readSearchRecord(ctx, tr, searchClaimKey(string(work.Class), bucket, work.OrgID, work.NodeID), &claim)
	if err != nil || !found {
		return record, claimMismatch(err)
	}
	if claim.Owner != work.Owner || claim.Generation != work.Generation || claim.Target != work.Target || !claim.LeaseUntil.After(s.clock.Now()) {
		return record, searchdomain.ErrWorkChanged
	}
	serving, err := tr.Get(fdb.Key(searchIndexKey())).Get()
	if err != nil {
		return record, searchReadFailure(ctx, "read serving search index", err)
	}
	if string(serving) != work.Target {
		telemetry.L(ctx).InfoContext(ctx, "search.work.target_changed", slog.String("index", work.Target), slog.String("node_id", work.NodeID.String()))
		return record, searchdomain.ErrWorkChanged
	}
	return record, nil
}

func claimMismatch(err error) error {
	if err != nil {
		return err
	}
	return searchdomain.ErrWorkChanged
}

func desiredGenerationKey(class searchdomain.WorkClass, orgID, nodeID uuid.UUID) []byte {
	switch class {
	case searchdomain.WorkClassRescan:
		return searchScanKey(orgID)
	case searchdomain.WorkClassRollout:
		return searchRolloutGenerationKey(orgID)
	case searchdomain.WorkClassLive, searchdomain.WorkClassAccess, searchdomain.WorkClassCleanup:
	}
	return searchGenerationKey(orgID, nodeID)
}
