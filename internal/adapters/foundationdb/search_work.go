package foundationdb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
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
	// rotation advances for each Claim that finds pending buckets and
	// selects its starting position among the oldest claimBucketChoices
	// buckets. Workers sharing the store use the same counter for
	// consecutive and concurrent claims.
	rotation atomic.Int64
}

// NewSearchWorkStore creates the FoundationDB search work adapter and
// starts the claim rotation counter at the injected clock's current time
// in Unix nanoseconds. The clock also sets and checks claim leases.
func NewSearchWorkStore(db fdb.Database, source clock.Clock) *SearchWorkStore {
	store := &SearchWorkStore{db: db, clock: source, rotation: atomic.Int64{}}
	store.rotation.Store(source.Now().UnixNano())
	return store
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

// Claim leases one item of the requested class. A rescan claim first runs
// one bounded step of the projection sweep. One read finds pending buckets
// and each bucket's first age key. The store rotation selects the first
// bucket among the claimBucketChoices buckets with the oldest first items.
// Claim tries the rest of those buckets in rotation order, then the
// remaining buckets oldest first. Within each bucket, it claims the oldest
// claimable item.
//
// Claim returns false and a nil error when the pending buckets do not
// contain a claimable item. The store.search_work.claim operation records
// an empty claim as a success.
func (s *SearchWorkStore) Claim(ctx context.Context, class searchdomain.WorkClass, owner string, lease time.Duration) (work searchdomain.Work, claimed bool, err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.claim")(&err)
	var none searchdomain.Work
	if owner == "" || lease <= 0 {
		return none, false, searchStorageError(ctx, "search.work.claim_invalid", "validate claim owner and lease", uuid.Nil, errors.New("owner and positive lease are required"))
	}
	if class == searchdomain.WorkClassRescan {
		if err := s.sweepProjections(ctx); err != nil {
			return none, false, searchStorageError(ctx, "search.projection.sweep_failed", "sweep organization projections", uuid.Nil, err)
		}
	}
	pending, err := s.pendingBuckets(ctx, class)
	if err != nil || len(pending) == 0 {
		return none, false, err
	}
	for _, bucket := range claimOrder(pending, s.rotation.Add(1)) {
		work, claimed, err = s.claimBucket(ctx, class, bucket, owner, lease)
		if err != nil {
			return none, false, err
		}
		if claimed {
			return work, true, nil
		}
	}
	return none, false, nil
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

// pendingBuckets returns each bucket of class that has an age key and the
// part of its first age key after the bucket prefix.
// It first calls [classHasAgeKey] in the same transaction. If the class
// does not have an age key, it returns an empty list without probing buckets.
// Otherwise, it issues one key selector per bucket across all 256 buckets.
// Every read in the transaction uses a snapshot read. Snapshot reads do not
// add read conflict ranges.
func (s *SearchWorkStore) pendingBuckets(ctx context.Context, class searchdomain.WorkClass) ([]pendingBucket, error) {
	pending := make([]pendingBucket, 0, searchWorkBuckets)
	err := transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		pending = pending[:0]
		classPending, probeErr := classHasAgeKey(ctx, tr, class)
		if probeErr != nil || !classPending {
			return probeErr
		}
		firstKeys := make([]fdb.FutureKey, searchWorkBuckets)
		prefixes := make([]int, searchWorkBuckets)
		ends := make([]fdb.Key, searchWorkBuckets)
		for bucket := range searchWorkBuckets {
			prefix := searchAgePrefix(string(class), bucket)
			keyRange, err := fdb.PrefixRange(prefix)
			if err != nil {
				return searchReadFailure(ctx, "create search age range", err)
			}
			firstKeys[bucket] = tr.Snapshot().GetKey(fdb.FirstGreaterOrEqual(keyRange.Begin))
			prefixes[bucket] = len(prefix)
			ends[bucket] = keyRange.End.FDBKey()
		}
		for bucket, firstKey := range firstKeys {
			key, err := firstKey.Get()
			if err != nil {
				return searchReadFailure(ctx, "probe search age bucket", err)
			}
			if bytes.Compare(key, ends[bucket]) < 0 {
				pending = append(pending, pendingBucket{bucket: bucket, oldest: key[prefixes[bucket]:]})
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
