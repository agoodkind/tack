package foundationdb

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// LookupIdempotencyKey returns the record stored under key, or nil when no record exists.
func (s *NodeStore) LookupIdempotencyKey(ctx context.Context, orgID uuid.UUID, key string) (record *node.IdempotencyRecord, err error) {
	defer telemetry.FDBOp(ctx, "store.node.lookup_idempotency")(&err)
	var encoded []byte
	err = runNodeReadTransaction(ctx, s.db, "node.lookup_idempotency", func(tr fdb.Transaction) error {
		var readErr error
		encoded, readErr = tr.Get(fdb.Key(idempotencyKey(orgID, key))).Get()
		if readErr != nil {
			wrapped := fmt.Errorf("read idempotency key: %w", readErr)
			telemetry.L(ctx).ErrorContext(ctx, "node.idempotency.read_failed", slog.String("err", wrapped.Error()), slog.String("org_id", orgID.String()))
			return loggedSearchError{err: wrapped}
		}
		return nil
	})
	if err != nil {
		if searchFailureWasLogged(err) {
			return nil, err
		}
		wrapped := fmt.Errorf("fdb lookup idempotency: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "node.idempotency.lookup_failed", slog.String("err", wrapped.Error()), slog.String("org_id", orgID.String()))
		return nil, wrapped
	}
	if len(encoded) == 0 {
		return nil, nil
	}
	record, decodeErr := decodeIdempotencyRecord(key, encoded)
	if decodeErr != nil {
		wrapped := fmt.Errorf("decode idempotency record: %w", decodeErr)
		telemetry.L(ctx).ErrorContext(ctx, "node.idempotency.decode_failed", slog.String("err", wrapped.Error()), slog.String("org_id", orgID.String()))
		return nil, wrapped
	}
	return record, nil
}
