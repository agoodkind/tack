package datagen

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// searchAgeFamily is the FoundationDB key family for pending search work.
	// Each key uses (search_age, class, bucket, enqueuedUnixNano, orgID, nodeID).
	searchAgeFamily      = "search_age"
	searchAgeKeyElements = 6
	searchAgeBucket      = 2
	searchAgeEnqueued    = 3
)

var errSearchAgeKey = errors.New("pending search work key has an unexpected layout")

// searchMixedBacklog reads the enqueue time of the oldest pending search work of each class.
type searchMixedBacklog struct {
	db      fdb.Database
	timeout time.Duration
}

func newSearchMixedBacklog(ctx context.Context, cfg *config.Config) (*searchMixedBacklog, error) {
	db, err := fdbadapter.Open(cfg.FDBClusterFile, cfg.FDBTransactionTimeout)
	if err != nil {
		return nil, loggedError(ctx, "qa datagen search-mixed: the command could not open FoundationDB to read pending search work", err)
	}
	return &searchMixedBacklog{db: db, timeout: cfg.FDBTransactionTimeout}, nil
}

// oldest returns the enqueue time of the oldest pending work item in each nonempty class.
// It uses snapshot reads to read the first key in each nonempty bucket.
func (b *searchMixedBacklog) oldest(ctx context.Context) (map[searchdomain.WorkClass]time.Time, error) {
	transaction, err := b.db.CreateTransaction()
	if err != nil {
		return nil, loggedError(ctx, "qa datagen search-mixed: the command could not create a transaction to read pending search work", err)
	}
	defer transaction.Cancel()
	if err := transaction.Options().SetTimeout(max(b.timeout.Milliseconds(), 1)); err != nil {
		return nil, loggedError(ctx, "qa datagen search-mixed: the command could not set the transaction timeout for pending search work", err)
	}
	for {
		oldest, readErr := readOldestSearchWork(ctx, transaction.Snapshot())
		if readErr == nil {
			return oldest, nil
		}
		var databaseError fdb.Error
		if !errors.As(readErr, &databaseError) {
			return nil, loggedError(ctx, "qa datagen search-mixed: the command could not read pending search work", readErr)
		}
		if retryErr := transaction.OnError(databaseError).Get(); retryErr != nil {
			return nil, loggedError(ctx, "qa datagen search-mixed: the command could not retry the read of pending search work", retryErr)
		}
	}
}

func readOldestSearchWork(ctx context.Context, snapshot fdb.Snapshot) (map[searchdomain.WorkClass]time.Time, error) {
	prefix := fdbadapter.TestPrefixRange()
	oldest := make(map[searchdomain.WorkClass]time.Time)
	for _, class := range searchdomain.ScheduledWorkClasses() {
		classRange, err := fdb.PrefixRange(prefixedKey(prefix, tuple.Tuple{searchAgeFamily, string(class)}))
		if err != nil {
			return nil, loggedError(ctx, "the command could not create a key range for pending search work in class "+string(class), err)
		}
		begin := classRange.Begin.FDBKey()
		end := classRange.End.FDBKey()
		for {
			key, err := snapshot.GetKey(fdb.FirstGreaterOrEqual(begin)).Get()
			if err != nil {
				return nil, loggedError(ctx, "the command could not read a pending search work key for class "+string(class), err)
			}
			if bytes.Compare(key, end) >= 0 {
				break
			}
			bucket, enqueued, err := unpackSearchAgeKey(ctx, bytes.TrimPrefix(key, prefix))
			if err != nil {
				return nil, err
			}
			if current, found := oldest[class]; !found || enqueued.Before(current) {
				oldest[class] = enqueued
			}
			begin = fdb.Key(prefixedKey(prefix, tuple.Tuple{searchAgeFamily, string(class), bucket + 1}))
		}
	}
	return oldest, nil
}

func prefixedKey(prefix []byte, key tuple.Tuple) []byte {
	return append(bytes.Clone(prefix), key.Pack()...)
}

func unpackSearchAgeKey(ctx context.Context, key []byte) (int64, time.Time, error) {
	elements, err := tuple.Unpack(key)
	if err != nil {
		return 0, time.Time{}, loggedError(ctx, "the command could not decode the pending search work key", err)
	}
	if len(elements) != searchAgeKeyElements {
		return 0, time.Time{}, errSearchAgeKey
	}
	bucket, bucketValid := elements[searchAgeBucket].(int64)
	enqueued, enqueuedValid := elements[searchAgeEnqueued].(int64)
	if !bucketValid || !enqueuedValid {
		return 0, time.Time{}, errSearchAgeKey
	}
	return bucket, time.Unix(0, enqueued), nil
}
