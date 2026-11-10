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
//
// The store uses one environment key for model repair claims across Tack
// processes.
//
//   - Each call returns a new adapter with s.db.
func (s *Stores) SearchModelRepairs() *SearchModelRepairStore {
	return &SearchModelRepairStore{db: s.db}
}

// ClaimModelRepair counts one attempt for request.ModelID in one
// transaction.
//
// A record for another model starts a new episode and the granted claim
// replaces it. An existing episode refuses a claim when its attempt count
// equals or exceeds request.MaxAttempts or before request.Spacing elapses
// after the last attempt.
//
//   - A new episode starts at request.Now with one attempt.
//   - A continuing episode preserves EpisodeStarted and updates LastAttempt.
//   - A model change records the old model ID in Replaced without saving
//     that model's record in Previous.
func (s *SearchModelRepairStore) ClaimModelRepair(
	ctx context.Context,
	request searchdomain.ModelRepairRequest,
) (claim searchdomain.ModelRepairClaim, err error) {
	defer telemetry.FDBOp(ctx, "store.search_model_repair.claim")(&err)
	err = transactSearch(
		ctx,
		s.db,
		func(tr fdb.Transaction) error {
			claim = searchdomain.ModelRepairClaim{
				Granted: false,
				Record: searchdomain.ModelRepairRecord{
					ModelID:        "",
					EpisodeStarted: time.Time{},
					Attempts:       0,
					LastAttempt:    time.Time{},
				},
				Previous: searchdomain.ModelRepairRecord{
					ModelID:        "",
					EpisodeStarted: time.Time{},
					Attempts:       0,
					LastAttempt:    time.Time{},
				},
				PreviousFound: false,
				Replaced:      "",
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

			next := searchModelRepairRecord{
				ModelID:        request.ModelID,
				EpisodeStarted: request.Now,
				Attempts:       1,
				LastAttempt:    request.Now,
			}
			if found {
				claim.Previous, claim.PreviousFound = modelRepairRecordFor(stored), true
				if stored.Attempts >= request.MaxAttempts ||
					request.Now.Sub(stored.LastAttempt) < request.Spacing {
					claim.Record = claim.Previous
					return nil
				}

				next = searchModelRepairRecord{
					ModelID:        stored.ModelID,
					EpisodeStarted: stored.EpisodeStarted,
					Attempts:       stored.Attempts + 1,
					LastAttempt:    request.Now,
				}
			}

			claim.Granted, claim.Record = true, modelRepairRecordFor(next)

			return writeSearchRecord(ctx, tr, searchModelRepairKey(), next)
		},
	)
	if err != nil {
		return claim, searchStorageError(
			ctx,
			"search.model_repair.claim_failed",
			"claim model repair for "+request.ModelID,
			uuid.Nil,
			err,
		)
	}

	return claim, nil
}

// ReleaseModelRepair restores claim.Previous when claim.PreviousFound is
// true. Otherwise, it deletes the current record.
//
// The function changes the record only when it still equals claim.Record. A
// claim for a different model does not retain the replaced model's record
// for restoration.
//
//   - Equality requires the same model ID, episode start, attempt count, and
//     last attempt time.
func (s *SearchModelRepairStore) ReleaseModelRepair(
	ctx context.Context,
	claim searchdomain.ModelRepairClaim,
) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_model_repair.release")(&err)
	err = transactSearch(
		ctx,
		s.db,
		func(tr fdb.Transaction) error {
			var stored searchModelRepairRecord
			found, readErr := readSearchRecord(ctx, tr, searchModelRepairKey(), &stored)
			if readErr != nil || !found ||
				!sameModelRepairRecord(modelRepairRecordFor(stored), claim.Record) {
				return readErr
			}

			if !claim.PreviousFound {
				tr.Clear(fdb.Key(searchModelRepairKey()))
				return nil
			}

			return writeSearchRecord(
				ctx,
				tr,
				searchModelRepairKey(),
				storedModelRepairRecord(claim.Previous),
			)
		},
	)
	if err != nil {
		return searchStorageError(
			ctx,
			"search.model_repair.release_failed",
			"release model repair for "+claim.Record.ModelID,
			uuid.Nil,
			err,
		)
	}

	return nil
}

// UncountModelRepair removes the attempt that claim counted and preserves
// the episode start and claim time.
//
// The function does not change the record and reports false when its model
// ID, episode start, attempt count, or claim time differs from claim.Record.
//
//   - An absent record also returns false.
//   - A record with fewer than one attempt also returns false.
func (s *SearchModelRepairStore) UncountModelRepair(
	ctx context.Context,
	claim searchdomain.ModelRepairClaim,
) (uncounted bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_model_repair.uncount")(&err)
	err = transactSearch(
		ctx,
		s.db,
		func(tr fdb.Transaction) error {
			uncounted = false
			var stored searchModelRepairRecord
			found, readErr := readSearchRecord(ctx, tr, searchModelRepairKey(), &stored)
			if readErr != nil || !found ||
				!sameModelRepairRecord(modelRepairRecordFor(stored), claim.Record) ||
				stored.Attempts < 1 {
				return readErr
			}

			stored.Attempts--
			uncounted = true

			return writeSearchRecord(ctx, tr, searchModelRepairKey(), stored)
		},
	)
	if err != nil {
		return false, searchStorageError(
			ctx,
			"search.model_repair.uncount_failed",
			"uncount model repair for "+claim.Record.ModelID,
			uuid.Nil,
			err,
		)
	}

	return uncounted, nil
}

// ResetModelRepair deletes the model repair record and returns it.
//
// The function reports false when the record does not exist.
//
//   - A returned record uses UTC for EpisodeStarted and LastAttempt.
func (s *SearchModelRepairStore) ResetModelRepair(
	ctx context.Context,
) (record searchdomain.ModelRepairRecord, found bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_model_repair.reset")(&err)
	err = transactSearch(
		ctx,
		s.db,
		func(tr fdb.Transaction) error {
			var stored searchModelRepairRecord
			var readErr error
			found, readErr = readSearchRecord(ctx, tr, searchModelRepairKey(), &stored)
			if readErr != nil || !found {
				return readErr
			}

			record = modelRepairRecordFor(stored)
			tr.Clear(fdb.Key(searchModelRepairKey()))

			return nil
		},
	)
	if err != nil {
		return record, false, searchStorageError(
			ctx,
			"search.model_repair.reset_failed",
			"reset model repair",
			uuid.Nil,
			err,
		)
	}

	return record, found, nil
}

func modelRepairRecordFor(stored searchModelRepairRecord) searchdomain.ModelRepairRecord {
	return searchdomain.ModelRepairRecord{
		ModelID:        stored.ModelID,
		EpisodeStarted: stored.EpisodeStarted.UTC(),
		Attempts:       stored.Attempts,
		LastAttempt:    stored.LastAttempt.UTC(),
	}
}

func sameModelRepairRecord(left, right searchdomain.ModelRepairRecord) bool {
	return left.ModelID == right.ModelID && left.Attempts == right.Attempts &&
		left.EpisodeStarted.Equal(right.EpisodeStarted) && left.LastAttempt.Equal(right.LastAttempt)
}

func storedModelRepairRecord(record searchdomain.ModelRepairRecord) searchModelRepairRecord {
	return searchModelRepairRecord{
		ModelID:        record.ModelID,
		EpisodeStarted: record.EpisodeStarted,
		Attempts:       record.Attempts,
		LastAttempt:    record.LastAttempt,
	}
}
