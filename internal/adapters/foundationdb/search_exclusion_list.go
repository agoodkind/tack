package foundationdb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// maxExclusionPage bounds the exclusions one listing read returns.
const maxExclusionPage = 100

// SearchExclusions reads one page of at most maxExclusionPage exclusions of
// every organization after cursor. An empty cursor starts at the first
// exclusion.
func (s *Stores) SearchExclusions(ctx context.Context, cursor string) (page searchdomain.ExclusionPage, err error) {
	defer telemetry.FDBOp(ctx, "store.search_exclusion.list")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		items, readErr := readExclusionItems(ctx, tr, cursor)
		if readErr != nil {
			return readErr
		}
		examined := items[:min(len(items), maxExclusionPage)]
		page = searchdomain.ExclusionPage{Exclusions: make([]searchdomain.Exclusion, 0, len(examined)), NextCursor: "", Done: len(items) <= maxExclusionPage}
		for _, item := range examined {
			exclusion, decodeErr := decodeExclusion(ctx, item)
			if decodeErr != nil {
				return decodeErr
			}
			page.Exclusions = append(page.Exclusions, exclusion)
		}
		if !page.Done {
			page.NextCursor = base64.RawURLEncoding.EncodeToString(examined[len(examined)-1].Key)
		}
		return nil
	})
	if err != nil {
		return page, searchStorageError(ctx, "search.exclusion.list_failed", "list search exclusions", uuid.Nil, err)
	}
	return page, nil
}

// readExclusionItems reads at most one more than a page of exclusion records
// after cursor. The extra record reports whether another page exists.
func readExclusionItems(ctx context.Context, tr fdb.Transaction, cursor string) ([]fdb.KeyValue, error) {
	keyRange, err := fdb.PrefixRange(searchExclusionFamilyPrefix())
	if err != nil {
		return nil, searchReadFailure(ctx, "create search exclusion range", err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	if cursor != "" {
		lastKey, decodeErr := base64.RawURLEncoding.DecodeString(cursor)
		if decodeErr != nil {
			return nil, searchReadFailure(ctx, "decode search exclusion cursor", decodeErr)
		}
		begin = fdb.FirstGreaterThan(fdb.Key(lastKey))
	}
	selection := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}
	items, err := tr.GetRange(selection, fdb.RangeOptions{Limit: maxExclusionPage + 1}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read search exclusion page", err)
	}
	return items, nil
}

// decodeExclusion decodes (search_exclusion, orgID, nodeID) and its record.
func decodeExclusion(ctx context.Context, item fdb.KeyValue) (searchdomain.Exclusion, error) {
	var none searchdomain.Exclusion
	values, err := tuple.Unpack(stripPrefix(item.Key))
	if err != nil {
		return none, searchReadFailure(ctx, "unpack search exclusion key", err)
	}
	if len(values) != 3 {
		return none, searchReadFailure(ctx, "decode search exclusion key", fmt.Errorf("search exclusion key has %d values", len(values)))
	}
	orgText, orgOK := values[1].(string)
	nodeText, nodeOK := values[2].(string)
	orgID, orgErr := uuid.Parse(orgText)
	nodeID, nodeErr := uuid.Parse(nodeText)
	if !orgOK || !nodeOK || orgErr != nil || nodeErr != nil {
		return none, searchReadFailure(ctx, "decode search exclusion key", errors.New("search exclusion key identity is invalid"))
	}
	var record searchExclusionRecord
	if err := json.Unmarshal(item.Value, &record); err != nil {
		return none, searchReadFailure(ctx, "decode search exclusion of node "+nodeID.String(), err)
	}
	return searchdomain.Exclusion{
		OrgID: orgID, NodeID: nodeID, Class: searchdomain.WorkClass(record.Class),
		Index: record.Index, Reason: record.Reason, ExcludedAt: record.ExcludedAt,
	}, nil
}
