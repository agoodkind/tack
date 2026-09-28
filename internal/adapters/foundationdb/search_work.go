package foundationdb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// searchRetryDelay is how long a released work item waits before another
// claim can retry it.
const searchRetryDelay = 5 * time.Second

// SearchWorkStore persists generation-bound search work in FoundationDB.
type SearchWorkStore struct {
	db    fdb.Database
	clock clock.Clock
}

// NewSearchWorkStore creates the FoundationDB search work adapter. The clock
// sets and checks claim leases.
func NewSearchWorkStore(db fdb.Database, source clock.Clock) *SearchWorkStore {
	return &SearchWorkStore{db: db, clock: source}
}

// InitializeSearchIndex records the serving physical index that every claim
// targets. Recording the same index again succeeds. The operation refuses
// to replace a different recorded index.
func (s *Stores) InitializeSearchIndex(ctx context.Context, index string) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.initialize_index")(&err)
	if index == "" {
		return searchStorageError(ctx, "search.index.invalid", "record serving search index", uuid.Nil, errors.New("index name is required"))
	}
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		current, readErr := tr.Get(fdb.Key(searchIndexKey())).Get()
		if readErr != nil {
			return searchReadFailure(ctx, "read serving search index", readErr)
		}
		if len(current) != 0 && string(current) != index {
			return errors.New("serving search index is already " + string(current))
		}
		tr.Set(fdb.Key(searchIndexKey()), []byte(index))
		return nil
	})
	if err != nil {
		return searchStorageError(ctx, "search.index.record_failed", "record serving search index "+index, uuid.Nil, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.index.recorded", slog.String("index", index))
	return nil
}

// Claim leases one item of the requested class. One read finds the buckets
// that contain pending work. The clock selects the index of the first
// pending bucket to try, and Claim tries the remaining pending buckets in
// order after it. Workers that call Claim at different times can start at
// different pending buckets.
func (s *SearchWorkStore) Claim(ctx context.Context, class searchdomain.WorkClass, owner string, lease time.Duration) (work searchdomain.Work, err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.claim")(&err)
	var none searchdomain.Work
	if owner == "" || lease <= 0 {
		return none, searchStorageError(ctx, "search.work.claim_invalid", "validate claim owner and lease", uuid.Nil, errors.New("owner and positive lease are required"))
	}
	pending, err := s.pendingBuckets(ctx, class)
	if err != nil {
		return none, err
	}
	if len(pending) == 0 {
		return none, searchdomain.ErrNoWork
	}
	// A clock before the Unix epoch gives a negative remainder.
	start := int(s.clock.Now().UnixNano() % int64(len(pending)))
	if start < 0 {
		start += len(pending)
	}
	for offset := range pending {
		work, err = s.claimBucket(ctx, class, pending[(start+offset)%len(pending)], owner, lease)
		if err == nil {
			return work, nil
		}
		if !errors.Is(err, searchdomain.ErrNoWork) {
			return none, err
		}
	}
	return none, searchdomain.ErrNoWork
}

// claimFirstItem claims the first current work item among the age keys in
// items. It clears each age key of a stale record it reads before that item.
func (s *SearchWorkStore) claimFirstItem(
	ctx context.Context,
	tr fdb.Transaction,
	class searchdomain.WorkClass,
	bucket int,
	owner string,
	lease time.Duration,
	scope claimScope,
	items []fdb.KeyValue,
) (searchdomain.Work, bool, error) {
	var none searchdomain.Work
	for _, item := range items {
		record, current, err := readAgedWork(ctx, tr, class, bucket, item.Key)
		if err != nil {
			return none, false, err
		}
		if !current {
			tr.Clear(item.Key)
			continue
		}
		candidate, claimed, err := s.claimWorkItem(ctx, tr, class, owner, lease, scope, record)
		if err != nil || claimed {
			return candidate, claimed, err
		}
	}
	return none, false, nil
}

// pendingBuckets returns the buckets of class that contain at least one age
// key. It issues one key selector per bucket in one transaction. The
// selectors use snapshot reads, and snapshot reads add no read conflict
// ranges.
func (s *SearchWorkStore) pendingBuckets(ctx context.Context, class searchdomain.WorkClass) ([]int, error) {
	pending := make([]int, 0, searchWorkBuckets)
	err := transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		pending = pending[:0]
		firstKeys := make([]fdb.FutureKey, searchWorkBuckets)
		ends := make([]fdb.Key, searchWorkBuckets)
		for bucket := range searchWorkBuckets {
			keyRange, err := fdb.PrefixRange(searchAgePrefix(string(class), bucket))
			if err != nil {
				return searchReadFailure(ctx, "create search age range", err)
			}
			firstKeys[bucket] = tr.Snapshot().GetKey(fdb.FirstGreaterOrEqual(keyRange.Begin))
			ends[bucket] = keyRange.End.FDBKey()
		}
		for bucket, firstKey := range firstKeys {
			key, err := firstKey.Get()
			if err != nil {
				return searchReadFailure(ctx, "probe search age bucket", err)
			}
			if bytes.Compare(key, ends[bucket]) < 0 {
				pending = append(pending, bucket)
			}
		}
		return nil
	})
	if err != nil {
		return nil, searchStorageError(ctx, "search.work.probe_failed", "probe search work buckets of class "+string(class), uuid.Nil, err)
	}
	return pending, nil
}

func searchStorageError(ctx context.Context, event, operation string, nodeID uuid.UUID, err error) error {
	if errors.Is(err, searchdomain.ErrWorkChanged) || errors.Is(err, searchdomain.ErrNoServingIndex) {
		return err
	}
	if searchFailureWasLogged(err) {
		return err
	}
	wrapped := fmt.Errorf("%s: %w", operation, err)
	telemetry.L(ctx).ErrorContext(ctx, event, slog.String("err", wrapped.Error()), slog.String("node_id", nodeID.String()))
	return loggedSearchError{err: wrapped}
}
