package foundationdb

import (
	"bytes"
	"context"
	"encoding/base64"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// affectedDocuments reads issued pages of the first nodes in the affected
// set in key order and returns at most limit documents. It starts after the
// verification cursor for the first node and reads at most limit plus one
// documents.
func (s *SearchAccessRolloutStore) affectedDocuments(ctx context.Context, work searchdomain.Work, read searchdomain.AccessRollout, limit int) ([]searchdomain.RolloutDocument, bool, error) {
	var items []fdb.KeyValue
	err := transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		items = nil
		nodes, err := readRolloutAffected(ctx, tr, work.OrgID, read.Generation, searchRolloutAffectedLimit)
		if err != nil {
			return err
		}
		for position, nodeID := range nodes {
			if len(items) > limit {
				return nil
			}
			cursor := ""
			if position == 0 {
				cursor = read.VerifyCursor
			}
			keyRange, begin, rangeErr := affectedPageRange(ctx, work.OrgID, nodeID, cursor)
			if rangeErr != nil {
				return rangeErr
			}
			page, readErr := tr.GetRange(fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(keyRange.End)}, fdb.RangeOptions{Limit: limit + 1 - len(items)}).GetSliceWithError()
			if readErr != nil {
				return searchReadFailure(ctx, "read issued documents of affected node "+nodeID.String(), readErr)
			}
			items = append(items, page...)
		}
		return nil
	})
	if err != nil {
		return nil, false, searchStorageError(ctx, "search.rollout.affected_documents_failed", "read affected node documents of authority "+work.OrgID.String(), uuid.Nil, err)
	}
	documents := make([]searchdomain.RolloutDocument, 0, min(len(items), limit))
	for _, item := range items[:min(len(items), limit)] {
		document, decodeErr := decodeRolloutDocument(ctx, item)
		if decodeErr != nil {
			return nil, false, searchStorageError(ctx, "search.rollout.document_invalid", "decode affected node document of authority "+work.OrgID.String(), uuid.Nil, decodeErr)
		}
		documents = append(documents, document)
	}
	return documents, len(items) <= limit, nil
}

// affectedPageRange returns one node's issued page range and the selector
// after the cursor. A cursor for another node selects the first page.
func affectedPageRange(ctx context.Context, orgID, nodeID uuid.UUID, cursor string) (fdb.KeyRange, fdb.KeySelector, error) {
	prefix := searchIssuedPrefix(orgID, nodeID)
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		return keyRange, fdb.FirstGreaterOrEqual(nil), searchReadFailure(ctx, "create page range of affected node "+nodeID.String(), err)
	}
	begin := fdb.FirstGreaterOrEqual(keyRange.Begin)
	if cursor == "" {
		return keyRange, begin, nil
	}
	lastKey, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return keyRange, begin, searchReadFailure(ctx, "decode cursor of affected node "+nodeID.String(), err)
	}
	if bytes.HasPrefix(lastKey, prefix) {
		begin = fdb.FirstGreaterThan(fdb.Key(lastKey))
	}
	return keyRange, begin, nil
}
