package foundationdb

import (
	"context"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// searchModelRepairRecord is the stored form of the one model repair record.
type searchModelRepairRecord struct {
	ModelID        string    `json:"model_id"`
	EpisodeStarted time.Time `json:"episode_started"`
	Attempts       int       `json:"attempts"`
	LastAttempt    time.Time `json:"last_attempt"`
}

// SearchModelRepairStore persists the model repair record under one key.
// Every Tack process claims repair attempts through it.
type SearchModelRepairStore struct {
	db fdb.Database
}

var _ searchdomain.ModelRepairStore = (*SearchModelRepairStore)(nil)

// SearchModelRepairs constructs the model repair store on the shared
// connection.
func (s *Stores) SearchModelRepairs() *SearchModelRepairStore {
	return &SearchModelRepairStore{db: s.db}
}

// ClaimModelRepair counts one attempt for request.ModelID in one
// transaction. A stored record for another model is deleted, and the claim
// starts a new episode. The claim is refused when the episode already has
// request.MaxAttempts attempts or when the previous attempt is younger than
// request.Spacing.
func (s *SearchModelRepairStore) ClaimModelRepair(ctx context.Context, request searchdomain.ModelRepairRequest) (claim searchdomain.ModelRepairClaim, err error) {
	defer telemetry.FDBOp(ctx, "store.search_model_repair.claim")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		claim = searchdomain.ModelRepairClaim{
			Granted: false, Record: searchdomain.ModelRepairRecord{ModelID: "", EpisodeStarted: time.Time{}, Attempts: 0, LastAttempt: time.Time{}},
			Previous:      searchdomain.ModelRepairRecord{ModelID: "", EpisodeStarted: time.Time{}, Attempts: 0, LastAttempt: time.Time{}},
			PreviousFound: false, Replaced: "",
		}
		var stored searchModelRepairRecord
		found, readErr := readSearchRecord(ctx, tr, searchModelRepairKey(), &stored)
		if readErr != nil {
			return readErr
		}
		if found && stored.ModelID != request.ModelID {
			claim.Replaced = stored.ModelID
			found = false
		}
		next := searchModelRepairRecord{ModelID: request.ModelID, EpisodeStarted: request.Now, Attempts: 1, LastAttempt: request.Now}
		if found {
			claim.Previous, claim.PreviousFound = modelRepairRecordFor(stored), true
			if stored.Attempts >= request.MaxAttempts || request.Now.Sub(stored.LastAttempt) < request.Spacing {
				claim.Record = claim.Previous
				return nil
			}
			next = searchModelRepairRecord{ModelID: stored.ModelID, EpisodeStarted: stored.EpisodeStarted, Attempts: stored.Attempts + 1, LastAttempt: request.Now}
		}
		claim.Granted, claim.Record = true, modelRepairRecordFor(next)
		return writeSearchRecord(ctx, tr, searchModelRepairKey(), next)
	})
	if err != nil {
		return claim, searchStorageError(ctx, "search.model_repair.claim_failed", "claim model repair for "+request.ModelID, uuid.Nil, err)
	}
	return claim, nil
}

// ReleaseModelRepair restores the record that claim replaced. It changes
// nothing when another claim changed the record after claim.
func (s *SearchModelRepairStore) ReleaseModelRepair(ctx context.Context, claim searchdomain.ModelRepairClaim) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_model_repair.release")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		var stored searchModelRepairRecord
		found, readErr := readSearchRecord(ctx, tr, searchModelRepairKey(), &stored)
		if readErr != nil || !found || !sameModelRepairRecord(modelRepairRecordFor(stored), claim.Record) {
			return readErr
		}
		if !claim.PreviousFound {
			tr.Clear(fdb.Key(searchModelRepairKey()))
			return nil
		}
		return writeSearchRecord(ctx, tr, searchModelRepairKey(), storedModelRepairRecord(claim.Previous))
	})
	if err != nil {
		return searchStorageError(ctx, "search.model_repair.release_failed", "release model repair for "+claim.Record.ModelID, uuid.Nil, err)
	}
	return nil
}

// UncountModelRepair removes the attempt that claim counted and keeps the
// episode start and claim time. It changes nothing and reports false when
// the stored record differs from claim.Record in model ID, episode start,
// attempt count, or claim time.
func (s *SearchModelRepairStore) UncountModelRepair(ctx context.Context, claim searchdomain.ModelRepairClaim) (uncounted bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_model_repair.uncount")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		uncounted = false
		var stored searchModelRepairRecord
		found, readErr := readSearchRecord(ctx, tr, searchModelRepairKey(), &stored)
		if readErr != nil || !found || !sameModelRepairRecord(modelRepairRecordFor(stored), claim.Record) || stored.Attempts < 1 {
			return readErr
		}
		stored.Attempts--
		uncounted = true
		return writeSearchRecord(ctx, tr, searchModelRepairKey(), stored)
	})
	if err != nil {
		return false, searchStorageError(ctx, "search.model_repair.uncount_failed", "uncount model repair for "+claim.Record.ModelID, uuid.Nil, err)
	}
	return uncounted, nil
}

// ResetModelRepair deletes the record and returns it. It reports false when
// no record exists.
func (s *SearchModelRepairStore) ResetModelRepair(ctx context.Context) (record searchdomain.ModelRepairRecord, found bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_model_repair.reset")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		var stored searchModelRepairRecord
		var readErr error
		found, readErr = readSearchRecord(ctx, tr, searchModelRepairKey(), &stored)
		if readErr != nil || !found {
			return readErr
		}
		record = modelRepairRecordFor(stored)
		tr.Clear(fdb.Key(searchModelRepairKey()))
		return nil
	})
	if err != nil {
		return record, false, searchStorageError(ctx, "search.model_repair.reset_failed", "reset model repair", uuid.Nil, err)
	}
	return record, found, nil
}

func modelRepairRecordFor(stored searchModelRepairRecord) searchdomain.ModelRepairRecord {
	return searchdomain.ModelRepairRecord{
		ModelID: stored.ModelID, EpisodeStarted: stored.EpisodeStarted.UTC(), Attempts: stored.Attempts, LastAttempt: stored.LastAttempt.UTC(),
	}
}

func sameModelRepairRecord(left, right searchdomain.ModelRepairRecord) bool {
	return left.ModelID == right.ModelID && left.Attempts == right.Attempts &&
		left.EpisodeStarted.Equal(right.EpisodeStarted) && left.LastAttempt.Equal(right.LastAttempt)
}

func storedModelRepairRecord(record searchdomain.ModelRepairRecord) searchModelRepairRecord {
	return searchModelRepairRecord{
		ModelID: record.ModelID, EpisodeStarted: record.EpisodeStarted, Attempts: record.Attempts, LastAttempt: record.LastAttempt,
	}
}
