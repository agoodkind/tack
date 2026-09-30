package foundationdb

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// maxAttemptPage bounds the attempt counts one listing read examines.
const maxAttemptPage = 100

// SearchStuckWork reads one page of at most maxAttemptPage attempt counts of
// every work class after cursor. The page lists each work item with a count
// at or past searchFailureLimit, and the last failure message of that item.
// An empty cursor starts at the first count.
func (s *Stores) SearchStuckWork(ctx context.Context, cursor string) (page searchdomain.StuckWorkPage, err error) {
	defer telemetry.FDBOp(ctx, "store.search_attempt.list")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		items, readErr := readAttemptItems(ctx, tr, cursor)
		if readErr != nil {
			return readErr
		}
		examined := items[:min(len(items), maxAttemptPage)]
		page = searchdomain.StuckWorkPage{Work: []searchdomain.StuckWork{}, NextCursor: "", Done: len(items) <= maxAttemptPage}
		for _, item := range examined {
			stuck, listed, decodeErr := decodeStuckWork(ctx, tr, item)
			if decodeErr != nil {
				return decodeErr
			}
			if listed {
				page.Work = append(page.Work, stuck)
			}
		}
		if !page.Done {
			page.NextCursor = base64.RawURLEncoding.EncodeToString(examined[len(examined)-1].Key)
		}
		return nil
	})
	if err != nil {
		return page, searchStorageError(ctx, "search.attempt.list_failed", "list search work attempt counts", uuid.Nil, err)
	}
	return page, nil
}

// readAttemptItems reads at most one more than a page of attempt counts
// after cursor. The extra count reports whether another page exists.
func readAttemptItems(ctx context.Context, tr fdb.Transaction, cursor string) ([]fdb.KeyValue, error) {
	keyRange, err := fdb.PrefixRange(withPrefix(tuple.Tuple{keySearchAttempt}.Pack()))
	if err != nil {
		return nil, searchReadFailure(ctx, "create search attempt range", err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	if cursor != "" {
		lastKey, decodeErr := base64.RawURLEncoding.DecodeString(cursor)
		if decodeErr != nil {
			return nil, searchReadFailure(ctx, "decode search attempt cursor", decodeErr)
		}
		begin = fdb.FirstGreaterThan(fdb.Key(lastKey))
	}
	selection := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}
	items, err := tr.GetRange(selection, fdb.RangeOptions{Limit: maxAttemptPage + 1}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read search attempt page", err)
	}
	return items, nil
}

// decodeStuckWork decodes (search_attempt, class, orgID, nodeID) and its
// count. It reports false for a count below searchFailureLimit.
func decodeStuckWork(ctx context.Context, tr fdb.Transaction, item fdb.KeyValue) (searchdomain.StuckWork, bool, error) {
	var none searchdomain.StuckWork
	attempts, err := unpackCounter(ctx, item.Value)
	if err != nil || attempts < searchFailureLimit {
		return none, false, err
	}
	values, err := tuple.Unpack(stripPrefix(item.Key))
	if err != nil {
		return none, false, searchReadFailure(ctx, "unpack search attempt key", err)
	}
	if len(values) != 4 {
		return none, false, searchReadFailure(ctx, "decode search attempt key", fmt.Errorf("search attempt key has %d values", len(values)))
	}
	class, classOK := values[1].(string)
	orgText, orgOK := values[2].(string)
	nodeText, nodeOK := values[3].(string)
	orgID, orgErr := uuid.Parse(orgText)
	nodeID, nodeErr := uuid.Parse(nodeText)
	if !classOK || !orgOK || !nodeOK || orgErr != nil || nodeErr != nil {
		return none, false, searchReadFailure(ctx, "decode search attempt key", errors.New("search attempt key identity is invalid"))
	}
	itemID, err := searchItemID(ctx, tr, searchdomain.WorkClass(class), orgID, nodeID)
	if err != nil {
		return none, false, err
	}
	message, err := tr.Get(fdb.Key(searchErrorKey(class, orgID, nodeID))).Get()
	if err != nil {
		return none, false, searchReadFailure(ctx, "read last search failure of "+class+" work "+itemID, err)
	}
	return searchdomain.StuckWork{Class: searchdomain.WorkClass(class), ItemID: itemID, Attempts: attempts, LastError: string(message)}, true, nil
}

// searchItemID returns the node ID of node work, the organization ID of
// rescan or rollout work, or the ID of the index replacement for rebuild
// work. A rebuild item without a replacement record returns an empty ID.
func searchItemID(ctx context.Context, tr fdb.Transaction, class searchdomain.WorkClass, orgID, nodeID uuid.UUID) (string, error) {
	if nodeID != uuid.Nil {
		return nodeID.String(), nil
	}
	if class != searchdomain.WorkClassRebuild {
		return orgID.String(), nil
	}
	rebuild, found, err := readRebuild(ctx, tr)
	if err != nil || !found {
		return "", err
	}
	return rebuild.ID.String(), nil
}
