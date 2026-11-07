package foundationdb

import (
	"encoding/json"
	"fmt"
	"log/slog"
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

// clearChildren clears children in key order and records each removed replay
// and visited value. After it clears limit records, it continues only through
// the remaining chunks of a query token run that started before that point.
// It clears the token chunks only when they match the chunk count and byte
// count of record. It returns the cleared token bytes as one value and the
// number of cleared records.
func (a *sessionAudit) clearChildren(tr fdb.Transaction, record searchSessionRecord, children []fdb.KeyValue, limit int) (json.RawMessage, int, error) {
	var tokens []byte
	var tokenKeys []fdb.Key
	contiguous := true
	cleared := 0
	for _, child := range children {
		unpacked, err := tuple.Unpack(stripPrefix(child.Key))
		if err != nil || len(unpacked) <= childIdentityPosition {
			return nil, 0, fmt.Errorf("decode child key %x of session %s", child.Key, record.ID)
		}
		part, _ := unpacked[childPartPosition].(string)
		if cleared >= limit && (part != sessionTokenPart || len(tokenKeys) == 0) {
			break
		}
		cleared++
		if part == sessionTokenPart {
			chunk, isChunk := unpacked[childIdentityPosition].(int64)
			contiguous = contiguous && isChunk && chunk == int64(len(tokenKeys))
			tokenKeys = append(tokenKeys, child.Key)
			tokens = append(tokens, child.Value...)
			continue
		}
		if err := a.recordChildDelete(record.ID, part, unpacked[childIdentityPosition], child.Value); err != nil {
			return nil, 0, err
		}
		tr.Clear(child.Key)
	}
	if len(tokenKeys) > 0 && (!contiguous || len(tokenKeys) != record.TokenChunks || len(tokens) != record.TokenBytes) {
		err := fmt.Errorf("session %s has %d token chunks of %d bytes, contiguous from chunk 0 %t, but its header records %d chunks of %d bytes",
			record.ID, len(tokenKeys), len(tokens), contiguous, record.TokenChunks, record.TokenBytes)
		slog.Error("search.session.token_chunks_invalid", slog.String("err", err.Error()), slog.String("session_id", record.ID.String()))
		return nil, 0, err
	}
	for _, key := range tokenKeys {
		tr.Clear(key)
	}
	return tokens, cleared, nil
}

func (a *sessionAudit) recordChildDelete(sessionID uuid.UUID, part string, identity tuple.TupleElement, stored []byte) error {
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
		return sessionStepError{operation: "decode replay record of session " + sessionID.String(), err: err}
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
