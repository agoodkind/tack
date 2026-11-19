package foundationdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
)

type pageBatch struct {
	entries []pageEntry
	lastKey fdb.Key
}

// readPageBatch reads one batch of at most pageBatchSize keys and returns the
// decoded entries plus the last raw key read. For ByProperty scans it reads
// the view for each index entry; for full scans the value is the view.
//
// db.ReadTransact is unavailable here: its closure must return an empty
// interface, and the repo's staticcheck rule refuses a new one. This is that
// retry loop spelled out, over a read transaction.
func (s *ViewStore) readPageBatch(
	ctx context.Context,
	q node.NodeListQuery,
	keyRange fdb.SelectorRange,
) ([]pageEntry, fdb.Key, error) {
	transaction, transactionErr := s.db.CreateTransaction()
	if transactionErr != nil {
		slog.ErrorContext(
			ctx,
			"store.view.list_page_transaction_failed",
			slog.String("err", transactionErr.Error()),
		)

		return nil, nil, fmt.Errorf(
			"create list page %s transaction: %w",
			q.NodeType,
			transactionErr,
		)
	}
	defer transaction.Cancel()

	for {
		batch, readErr := readPageBatchOnce(ctx, transaction, q, keyRange)
		if readErr == nil {
			return batch.entries, batch.lastKey, nil
		}

		var fdbErr fdb.Error
		if !errors.As(readErr, &fdbErr) {
			return nil, nil, readErr
		}

		if retryErr := transaction.OnError(fdbErr).Get(); retryErr != nil {
			slog.ErrorContext(
				ctx,
				"store.view.list_page_retry_failed",
				slog.String("err", retryErr.Error()),
			)

			return nil, nil, fmt.Errorf("retry list page %s batch: %w", q.NodeType, retryErr)
		}
	}
}

func readPageBatchOnce(
	ctx context.Context,
	tr fdb.ReadTransaction,
	q node.NodeListQuery,
	keyRange fdb.SelectorRange,
) (pageBatch, error) {
	kvs, err := tr.GetRange(keyRange, fdb.RangeOptions{Limit: pageBatchSize}).GetSliceWithError()
	if err != nil {
		slog.ErrorContext(ctx, "store.view.list_page_batch_failed", slog.String("err", err.Error()))
		return pageBatch{}, fmt.Errorf("list page %s batch: %w", q.NodeType, err)
	}

	entries := make([]pageEntry, 0, len(kvs))
	for _, kv := range kvs {
		entry, decodeErr := decodePageEntry(ctx, tr, q, kv)
		if decodeErr != nil {
			return pageBatch{}, decodeErr
		}

		entries = append(entries, entry)
	}

	var lastKey fdb.Key
	if len(kvs) > 0 {
		lastKey = kvs[len(kvs)-1].Key
	}

	return pageBatch{entries: entries, lastKey: lastKey}, nil
}

func decodePageEntry(
	ctx context.Context,
	tr fdb.ReadTransaction,
	q node.NodeListQuery,
	kv fdb.KeyValue,
) (pageEntry, error) {
	if q.ByProperty == nil && (q.BySourceRelation != nil || q.ByTargetRelation != nil) {
		return decodeRelationPageEntry(ctx, tr, q, kv)
	}

	if q.ByProperty == nil {
		var view node.NodeView
		if err := json.Unmarshal(kv.Value, &view); err != nil {
			slog.ErrorContext(
				ctx,
				"store.view.list_page_decode_failed",
				slog.String("err", err.Error()),
			)

			return pageEntry{}, fmt.Errorf("decode view %x: %w", kv.Key, err)
		}

		return pageEntry{id: view.ID, view: &view, key: kv.Key}, nil
	}

	nodeID, err := lastTupleID(ctx, kv.Key)
	if err != nil {
		return pageEntry{}, err
	}

	raw, err := tr.Get(fdb.Key(nodeViewKey(q.OrgID, q.NodeType, nodeID))).Get()
	if err != nil {
		slog.ErrorContext(
			ctx,
			"store.view.list_page_decode_failed",
			slog.String("err", err.Error()),
		)

		return pageEntry{}, fmt.Errorf("read view %s: %w", nodeID, err)
	}

	if len(raw) == 0 {
		return pageEntry{id: nodeID, view: nil, key: kv.Key}, nil
	}

	var view node.NodeView
	if err := json.Unmarshal(raw, &view); err != nil {
		slog.ErrorContext(
			ctx,
			"store.view.list_page_decode_failed",
			slog.String("err", err.Error()),
		)

		return pageEntry{}, fmt.Errorf("decode view %s: %w", nodeID, err)
	}

	return pageEntry{id: nodeID, view: &view, key: kv.Key}, nil
}
