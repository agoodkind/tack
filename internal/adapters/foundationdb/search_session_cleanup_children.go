package foundationdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
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

// clearChildren clears children in key order and records each removed replay and
// visited value. After limit records, it continues only through the remaining
// chunks of a query token run that started before that point. It clears token
// chunks only when they match the chunk count and byte count of record. It
// returns the cleared token bytes as one value and the number of cleared records.
func (a *sessionAudit) clearChildren(
	tr fdb.Transaction,
	record searchSessionRecord,
	children []fdb.KeyValue,
	limit int,
) (json.RawMessage, int, error) {
	var tokenChunks []fdb.KeyValue
	cleared := 0

	for _, child := range children {
		unpacked, err := tuple.Unpack(stripPrefix(child.Key))
		if err != nil || len(unpacked) <= childIdentityPosition {
			return nil, 0, fmt.Errorf("decode child key %x of session %s", child.Key, record.ID)
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

		if err := a.recordChildDelete(
			record.ID,
			part,
			unpacked[childIdentityPosition],
			child.Value,
		); err != nil {
			return nil, 0, err
		}

		tr.Clear(child.Key)
	}

	if len(tokenChunks) == 0 {
		return nil, cleared, nil
	}

	tokens, err := assembleSessionTokens(record, tokenChunks)
	if err != nil {
		return nil, 0, err
	}

	for _, chunk := range tokenChunks {
		tr.Clear(chunk.Key)
	}

	return tokens, cleared, nil
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

func (a *sessionAudit) recordChildDelete(
	sessionID uuid.UUID,
	part string,
	identity tuple.TupleElement,
	stored []byte,
) error {
	if part == sessionVisitedPart {
		text, _ := identity.(string)
		nodeID, err := uuid.Parse(text)
		if err != nil {
			return sessionStepError{operation: "decode visited node ID " + text, err: err}
		}

		return a.recordVisitedDelete(sessionID, nodeID)
	}

	if part != sessionReplayPart {
		return fmt.Errorf("session %s has an unknown child part %q", sessionID, part)
	}

	version, err := replayKeyVersion(identity)
	if err != nil {
		return err
	}

	var replay searchReplayRecord
	if err := json.Unmarshal(stored, &replay); err != nil {
		return sessionStepError{
			operation: "decode replay record of session " + sessionID.String(),
			err:       err,
		}
	}

	return a.recordReplayDelete(sessionID, version, replay)
}

// The tuple decoder returns int64 for a value that fits and uint64 otherwise.
func replayKeyVersion(identity tuple.TupleElement) (uint64, error) {
	version, err := strconv.ParseUint(fmt.Sprint(identity), 10, 64)
	if err != nil {
		return 0, sessionStepError{operation: "decode replay key version", err: err}
	}

	return version, nil
}
