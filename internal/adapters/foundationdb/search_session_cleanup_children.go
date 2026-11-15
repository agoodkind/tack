package foundationdb

import (
	"errors"
	"fmt"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
)

const (
	// childPartPosition and childIdentityPosition are the tuple positions of
	// the part and its identity in a child key:
	// (family, bucket, session, part, identity).
	childPartPosition     = 3
	childIdentityPosition = 4
)

// errSessionTokensInvalid marks stored query token chunks that do not match the
// chunk count and byte count in the session header.
var errSessionTokensInvalid = errors.New(
	"the stored query token chunks do not match the session header",
)

// clearChildren clears children in key order. After limit records, it
// continues only through the remaining
// chunks of a query token run that started before that point. It clears token
// chunks only when they match the chunk count and byte count of record. It
// returns the number of cleared records.
func clearChildren(
	tr fdb.Transaction,
	record searchSessionRecord,
	children []fdb.KeyValue,
	limit int,
) (int, error) {
	var tokenChunks []fdb.KeyValue
	cleared := 0

	for _, child := range children {
		unpacked, err := tuple.Unpack(stripPrefix(child.Key))
		if err != nil || len(unpacked) <= childIdentityPosition {
			return 0, fmt.Errorf("decode child key %x of session %s", child.Key, record.ID)
		}

		part, _ := unpacked[childPartPosition].(string)
		if cleared >= limit && (part != sessionTokenPart || len(tokenChunks) == 0) {
			break
		}

		cleared++
		if part == sessionTokenPart {
			tokenChunks = append(tokenChunks, child)
			continue
		}

		tr.Clear(child.Key)
	}

	if len(tokenChunks) == 0 {
		return cleared, nil
	}

	_, err := assembleSessionTokens(record, tokenChunks)
	if err != nil {
		return 0, err
	}

	for _, chunk := range tokenChunks {
		tr.Clear(chunk.Key)
	}

	return cleared, nil
}

// assembleSessionTokens joins chunks into the stored query tokens of record.
// It returns errSessionTokensInvalid unless the chunks start at chunk 0, have
// no gaps, and match the chunk count and byte count of record.
func assembleSessionTokens(record searchSessionRecord, chunks []fdb.KeyValue) ([]byte, error) {
	tokens := make([]byte, 0, min(max(record.TokenBytes, 0), maxSessionTokenBytes))
	contiguous := true

	for position, chunk := range chunks {
		unpacked, err := tuple.Unpack(stripPrefix(chunk.Key))
		contiguous = contiguous && err == nil && len(unpacked) > childIdentityPosition &&
			unpacked[childIdentityPosition] == int64(position)
		tokens = append(tokens, chunk.Value...)
	}

	if !contiguous || len(chunks) != record.TokenChunks || len(tokens) != record.TokenBytes {
		operation := fmt.Sprintf(
			"session %s has %d token chunks of %d bytes, contiguous from chunk 0 %t, and its header records %d chunks of %d bytes",
			record.ID,
			len(chunks),
			len(tokens),
			contiguous,
			record.TokenChunks,
			record.TokenBytes,
		)

		return nil, sessionStepError{operation: operation, err: errSessionTokensInvalid}
	}

	return tokens, nil
}

// readSliceChildren reads at most limit plus the token chunk count of record
// child records in one range read. A query token run that starts within the
// first limit records ends within that read. It returns errSessionTokensInvalid
// when record states a chunk count outside 0 to maxSessionTokenChunks.
func readSliceChildren(
	tr fdb.Transaction,
	record searchSessionRecord,
	limit int,
) ([]fdb.KeyValue, error) {
	if record.TokenChunks < 0 || record.TokenChunks > maxSessionTokenChunks {
		operation := fmt.Sprintf(
			"session %s header records %d token chunks, outside 0 to %d",
			record.ID,
			record.TokenChunks,
			maxSessionTokenChunks,
		)

		return nil, sessionStepError{operation: operation, err: errSessionTokensInvalid}
	}

	keyRange, err := fdb.PrefixRange(searchSessionChildPrefix(record.ID))
	if err != nil {
		return nil, sessionStepError{operation: "create child range", err: err}
	}

	countSessionRead(tr)
	children, err := tr.GetRange(
		keyRange,
		fdb.RangeOptions{Limit: limit + record.TokenChunks},
	).GetSliceWithError()
	if err != nil {
		return nil, sessionStepError{operation: "read child records", err: err}
	}

	return children, nil
}
