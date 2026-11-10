package foundationdb

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
)

// outboxCommitVersion decodes the versionstamp in one outbox key. It returns
// the 10-byte transaction version and the 2-byte big-endian user version as
// 24 lowercase hexadecimal characters.
func outboxCommitVersion(ctx context.Context, key []byte) (string, error) {
	values, err := tuple.Unpack(stripPrefix(key))
	if err != nil {
		return "", outboxCommitVersionFailure(ctx, err)
	}
	if len(values) != 2 {
		return "", outboxCommitVersionFailure(ctx, fmt.Errorf("the key has %d tuple values", len(values)))
	}
	stamp, ok := values[1].(tuple.Versionstamp)
	if !ok {
		return "", outboxCommitVersionFailure(ctx, errors.New("the key does not end with a versionstamp"))
	}
	encoded := make([]byte, 0, len(stamp.TransactionVersion)+2)
	encoded = append(encoded, stamp.TransactionVersion[:]...)
	encoded = binary.BigEndian.AppendUint16(encoded, stamp.UserVersion)
	return hex.EncodeToString(encoded), nil
}

// outboxCommitVersionFailure wraps err with the decoding operation, logs
// the wrapped error, and returns it.
func outboxCommitVersionFailure(ctx context.Context, err error) error {
	wrapped := fmt.Errorf("decode the commit version of an ops outbox key: %w", err)
	slog.ErrorContext(ctx, "ops_outbox.commit_version_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
