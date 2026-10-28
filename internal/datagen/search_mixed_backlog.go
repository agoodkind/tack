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
	searchAgeOrg         = 4
	searchAgeNode        = 5
)

var errSearchAgeKey = errors.New("pending search work key has an unexpected layout")

// searchMixedBacklog reads the enqueue time of the oldest pending search work of each class.
type searchMixedBacklog struct {
	db      fdb.Database
	timeout time.Duration
}

// searchClassBacklog records one reading of a class's pending search work: the oldest age key's
// enqueue time, the nonempty bucket count, and whether that key has a live work record.
type searchClassBacklog struct {
	oldest          time.Time
	nonemptyBuckets int
	oldestLive      bool
}

func newSearchMixedBacklog(ctx context.Context, cfg *config.Config) (*searchMixedBacklog, error) {
	db, err := fdbadapter.Open(cfg.FDBClusterFile, cfg.FDBTransactionTimeout)
	if err != nil {
		return nil, loggedError(ctx, "qa datagen search-mixed: the command could not open FoundationDB to read pending search work", err)
	}
	return &searchMixedBacklog{db: db, timeout: cfg.FDBTransactionTimeout}, nil
}

// pending returns pending search work keyed by class, omitting classes without age keys.
// It uses snapshot reads in one transaction and retries retryable FoundationDB errors.
func (b *searchMixedBacklog) pending(ctx context.Context) (map[searchdomain.WorkClass]searchClassBacklog, error) {
	transaction, err := b.db.CreateTransaction()
	if err != nil {
		return nil, loggedError(ctx, "qa datagen search-mixed: the command could not create a transaction to read pending search work", err)
	}
	defer transaction.Cancel()
	if err := transaction.Options().SetTimeout(max(b.timeout.Milliseconds(), 1)); err != nil {
		return nil, loggedError(ctx, "qa datagen search-mixed: the command could not set the transaction timeout for pending search work", err)
	}
	for {
		pending, readErr := readPendingSearchWork(ctx, transaction.Snapshot())
		if readErr == nil {
			return pending, nil
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

func readPendingSearchWork(ctx context.Context, snapshot fdb.Snapshot) (map[searchdomain.WorkClass]searchClassBacklog, error) {
	prefix := fdbadapter.TestPrefixRange()
	pending := make(map[searchdomain.WorkClass]searchClassBacklog)
	for _, class := range searchdomain.ScheduledWorkClasses() {
		backlog, found, err := readClassSearchWork(ctx, snapshot, prefix, class)
		if err != nil {
			return nil, err
		}
		if found {
			pending[class] = backlog
		}
	}
	return pending, nil
}

// readClassSearchWork reads each nonempty bucket's first age key for one class.
// It counts those buckets, selects the oldest key, and checks for its live work record.
// It returns false when the class does not have any age keys.
func readClassSearchWork(
	ctx context.Context, snapshot fdb.Snapshot, prefix []byte, class searchdomain.WorkClass,
) (searchClassBacklog, bool, error) {
	backlog := searchClassBacklog{oldest: time.Time{}, nonemptyBuckets: 0, oldestLive: false}
	classRange, err := fdb.PrefixRange(prefixedKey(prefix, tuple.Tuple{searchAgeFamily, string(class)}))
	if err != nil {
		return backlog, false, loggedError(ctx, "the command could not create a key range for pending search work in class "+string(class), err)
	}
	begin := classRange.Begin.FDBKey()
	end := classRange.End.FDBKey()
	var oldest searchAgeEntry
	for {
		key, err := snapshot.GetKey(fdb.FirstGreaterOrEqual(begin)).Get()
		if err != nil {
			return backlog, false, loggedError(ctx, "the command could not read a pending search work key for class "+string(class), err)
		}
		if bytes.Compare(key, end) >= 0 {
			break
		}
		entry, err := unpackSearchAgeKey(ctx, bytes.TrimPrefix(key, prefix))
		if err != nil {
			return backlog, false, err
		}
		if backlog.nonemptyBuckets == 0 || entry.enqueued < oldest.enqueued {
			oldest = entry
		}
		backlog.nonemptyBuckets++
		begin = fdb.Key(prefixedKey(prefix, tuple.Tuple{searchAgeFamily, string(class), entry.bucket + 1}))
	}
	if backlog.nonemptyBuckets == 0 {
		return backlog, false, nil
	}
	backlog.oldest = time.Unix(0, oldest.enqueued)
	backlog.oldestLive, err = searchAgeEntryLive(ctx, snapshot, prefix, class, oldest)
	if err != nil {
		return backlog, false, err
	}
	return backlog, true, nil
}

func prefixedKey(prefix []byte, key tuple.Tuple) []byte {
	return append(bytes.Clone(prefix), key.Pack()...)
}
