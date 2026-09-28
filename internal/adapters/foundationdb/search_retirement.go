package foundationdb

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// maxSearchDocumentBatch limits the issued IDs read by one access or cleanup slice.
const maxSearchDocumentBatch = 100

// Documents reads at most limit issued documents for work. Cleanup reads every
// revision older than the work revision, or every revision of a deleted node.
// Access reads every issued document of the node after the checkpointed
// document, including documents of older revisions. Access work updates
// every issued revision. Cleanup later retires the older revisions.
func (s *SearchWorkStore) Documents(ctx context.Context, work search.Work, limit int) (batch search.DocumentBatch, err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.documents")(&err)
	if limit < 1 || limit > maxSearchDocumentBatch {
		return batch, searchStorageError(ctx, "search.documents.limit_invalid", "validate document batch limit", work.NodeID, fmt.Errorf("limit must be between 1 and %d", maxSearchDocumentBatch))
	}
	selection, err := issuedDocumentRange(ctx, work)
	if err != nil {
		return batch, searchStorageError(ctx, "search.documents.range_failed", "select issued documents", work.NodeID, err)
	}
	var items []fdb.KeyValue
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		var readErr error
		items, readErr = tr.GetRange(selection, fdb.RangeOptions{Limit: limit + 1}).GetSliceWithError()
		if readErr != nil {
			return searchReadFailure(ctx, "read issued documents", readErr)
		}
		return nil
	})
	if err != nil {
		return batch, searchStorageError(ctx, "search.documents.read_failed", "read issued documents", work.NodeID, err)
	}
	batch = search.DocumentBatch{Documents: make([]search.IssuedDocument, 0, min(len(items), limit)), Done: len(items) <= limit}
	for index, item := range items {
		if index == limit {
			break
		}
		document, decodeErr := decodeIssuedDocument(ctx, item)
		if decodeErr != nil {
			return search.DocumentBatch{}, searchStorageError(ctx, "search.documents.decode_failed", "decode issued document", work.NodeID, decodeErr)
		}
		batch.Documents = append(batch.Documents, document)
	}
	return batch, nil
}

func issuedDocumentRange(ctx context.Context, work search.Work) (fdb.SelectorRange, error) {
	revision, err := strconv.ParseInt(work.Revision, 10, 64)
	if err != nil {
		return fdb.SelectorRange{}, searchReadFailure(ctx, "parse work revision", err)
	}
	all, err := fdb.PrefixRange(searchIssuedPrefix(work.OrgID, work.NodeID))
	if err != nil {
		return fdb.SelectorRange{}, searchReadFailure(ctx, "create issued range", err)
	}
	current, err := fdb.PrefixRange(searchIssuedRevisionPrefix(work.OrgID, work.NodeID, revision))
	if err != nil {
		return fdb.SelectorRange{}, searchReadFailure(ctx, "create issued revision range", err)
	}
	switch {
	case work.Class == search.WorkClassCleanup && work.Deleted:
		return fdb.SelectorRange{Begin: fdb.FirstGreaterOrEqual(all.Begin), End: fdb.FirstGreaterOrEqual(all.End)}, nil
	case work.Class == search.WorkClassCleanup:
		return fdb.SelectorRange{Begin: fdb.FirstGreaterOrEqual(all.Begin), End: fdb.FirstGreaterOrEqual(current.Begin)}, nil
	case work.Class == search.WorkClassAccess:
		begin := fdb.FirstGreaterOrEqual(all.Begin)
		if work.Cursor != "" {
			after, parseErr := issuedCursorKey(ctx, work)
			if parseErr != nil {
				return fdb.SelectorRange{}, parseErr
			}
			begin = fdb.FirstGreaterThan(fdb.Key(after))
		}
		return fdb.SelectorRange{Begin: begin, End: fdb.FirstGreaterOrEqual(all.End)}, nil
	default:
		return fdb.SelectorRange{}, errors.New("work class has no issued documents")
	}
}

// issuedCursor encodes the identity of the last checkpointed document.
func issuedCursor(document search.IssuedDocument) string {
	return base64.RawURLEncoding.EncodeToString(tuple.Tuple{document.Revision, document.Ordinal, document.Projection}.Pack())
}

// issuedCursorKey decodes an access cursor into the issued key of the last
// checkpointed document.
func issuedCursorKey(ctx context.Context, work search.Work) ([]byte, error) {
	encoded, err := base64.RawURLEncoding.DecodeString(work.Cursor)
	if err != nil {
		return nil, searchReadFailure(ctx, "decode issued cursor", err)
	}
	values, err := tuple.Unpack(encoded)
	if err != nil {
		return nil, searchReadFailure(ctx, "unpack issued cursor", err)
	}
	if len(values) != 3 {
		return nil, searchReadFailure(ctx, "decode issued cursor", fmt.Errorf("issued cursor has %d values", len(values)))
	}
	revision, revisionOK := values[0].(int64)
	ordinal, ordinalOK := values[1].(int64)
	projection, projectionOK := values[2].(string)
	if !revisionOK || !ordinalOK || !projectionOK || ordinal < 0 {
		return nil, searchReadFailure(ctx, "decode issued cursor", errors.New("issued cursor identity is invalid"))
	}
	return searchIssuedKey(work.OrgID, work.NodeID, revision, uint64(ordinal), projection), nil
}

func decodeIssuedDocument(ctx context.Context, item fdb.KeyValue) (search.IssuedDocument, error) {
	values, err := tuple.Unpack(stripPrefix(item.Key))
	if err != nil {
		return search.IssuedDocument{}, searchReadFailure(ctx, "unpack issued-document key", err)
	}
	if len(values) != 6 {
		return search.IssuedDocument{}, fmt.Errorf("issued-document key has %d values", len(values))
	}
	revision, revisionOK := values[3].(int64)
	ordinal, ordinalOK := values[4].(int64)
	projection, projectionOK := values[5].(string)
	if !revisionOK || !ordinalOK || !projectionOK || ordinal < 0 || len(item.Value) == 0 {
		return search.IssuedDocument{}, errors.New("issued-document identity is invalid")
	}
	return search.IssuedDocument{DocumentID: string(item.Value), Revision: revision, Ordinal: uint64(ordinal), Projection: projection}, nil
}

// CompleteRetirement clears each issued ID in accepted. OpenSearch accepted
// the retirement of those IDs. The final batch finishes the cleanup. For a
// deleted node it also clears every remaining search key of the node.
func (s *SearchWorkStore) CompleteRetirement(ctx context.Context, work search.Work, accepted []search.IssuedDocument, done bool) (err error) {
	defer telemetry.FDBOp(ctx, "store.search_work.complete_retirement")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		record, err := s.verifyClaim(ctx, tr, work)
		if err != nil {
			return err
		}
		for _, document := range accepted {
			tr.Clear(fdb.Key(searchIssuedKey(work.OrgID, work.NodeID, document.Revision, document.Ordinal, document.Projection)))
		}
		if len(accepted) > 0 {
			now := s.clock.Now()
			for _, index := range []string{work.Target, work.Mirror} {
				if err := recordRetiredSince(ctx, tr, index, now); err != nil {
					return err
				}
			}
		}
		if !done {
			return nil
		}
		if record.Deleted {
			return finishDeletedClass(ctx, tr, work, record)
		}
		clearSearchWork(tr, work, record)
		return nil
	})
	if err != nil {
		return searchStorageError(ctx, "search.retirement.checkpoint_failed", "checkpoint retired documents", work.NodeID, err)
	}
	return nil
}
