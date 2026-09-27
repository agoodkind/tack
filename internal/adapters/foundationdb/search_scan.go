package foundationdb

import (
	"bytes"
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

const maxSearchScanNodes = 100

// ScanSearch returns at most limit node IDs of orgID in FoundationDB key
// order after cursor.
func (s *NodeContentStore) ScanSearch(ctx context.Context, orgID uuid.UUID, cursor string, limit int) (scan searchdomain.ScanResult, err error) {
	defer telemetry.FDBOp(ctx, "store.search_content.scan")(&err)
	if limit <= 0 || limit > maxSearchScanNodes {
		err := fmt.Errorf("search scan limit must be between 1 and %d", maxSearchScanNodes)
		return scan, contentFailure(ctx, "search.scan.limit_invalid", "validate search scan", orgID, err)
	}
	keyRange, err := fdb.PrefixRange(nodeInstanceOrgPrefix(orgID))
	if err != nil {
		return scan, contentFailure(ctx, "search.scan.range_failed", "create search scan range", orgID, err)
	}
	begin, err := searchScanBegin(ctx, orgID, keyRange, cursor)
	if err != nil {
		return scan, err
	}
	var items []fdb.KeyValue
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		var readErr error
		selection := fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}
		items, readErr = tr.GetRange(selection, fdb.RangeOptions{Limit: limit + 1}).GetSliceWithError()
		if readErr != nil {
			return searchReadFailure(ctx, "read search scan page", readErr)
		}
		return nil
	})
	if err != nil {
		return scan, contentFailure(ctx, "search.scan.read_failed", "read search scan page", orgID, err)
	}
	return decodeSearchScan(ctx, orgID, items, limit)
}

// searchScanBegin returns the first key selector of a scan page. An empty
// cursor selects the start of keyRange. A cursor must decode to a key inside
// keyRange, and the page starts after that key.
func searchScanBegin(ctx context.Context, orgID uuid.UUID, keyRange fdb.KeyRange, cursor string) (fdb.KeySelector, error) {
	if cursor == "" {
		return fdb.FirstGreaterOrEqual(keyRange.Begin), nil
	}
	lastKey, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return fdb.KeySelector{}, contentFailure(ctx, "search.scan.cursor_invalid", "decode search scan cursor", orgID, err)
	}
	if bytes.Compare(lastKey, keyRange.Begin.FDBKey()) < 0 || bytes.Compare(lastKey, keyRange.End.FDBKey()) >= 0 {
		rangeErr := errors.New("cursor key is outside the organization node range")
		return fdb.KeySelector{}, contentFailure(ctx, "search.scan.cursor_invalid", "validate search scan cursor", orgID, rangeErr)
	}
	return fdb.FirstGreaterThan(fdb.Key(lastKey)), nil
}

func decodeSearchScan(ctx context.Context, orgID uuid.UUID, items []fdb.KeyValue, limit int) (searchdomain.ScanResult, error) {
	done := len(items) <= limit
	if !done {
		items = items[:limit]
	}
	scan := searchdomain.ScanResult{Nodes: make([]uuid.UUID, 0, len(items)), NextCursor: "", Done: done}
	for _, item := range items {
		values, err := tuple.Unpack(stripPrefix(item.Key))
		if err != nil {
			return searchdomain.ScanResult{}, contentFailure(ctx, "search.scan.key_invalid", "decode search scan key", orgID, err)
		}
		if len(values) < 4 {
			err := fmt.Errorf("node record key has %d tuple values", len(values))
			return searchdomain.ScanResult{}, contentFailure(ctx, "search.scan.key_invalid", "decode search scan key", orgID, err)
		}
		nodeIDText, ok := values[3].(string)
		if !ok {
			err := fmt.Errorf("node record key has no node identifier")
			return searchdomain.ScanResult{}, contentFailure(ctx, "search.scan.identity_invalid", "decode search scan node ID", orgID, err)
		}
		nodeID, err := uuid.Parse(nodeIDText)
		if err != nil {
			return searchdomain.ScanResult{}, contentFailure(ctx, "search.scan.identity_invalid", "parse search scan node ID", orgID, err)
		}
		scan.Nodes = append(scan.Nodes, nodeID)
	}
	if !done && len(items) > 0 {
		scan.NextCursor = base64.RawURLEncoding.EncodeToString(items[len(items)-1].Key)
	}
	return scan, nil
}
