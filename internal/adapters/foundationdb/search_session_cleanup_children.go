package foundationdb

import (
	"encoding/json"
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

// clearChildren clears the replay and visited records in children and records
// each removed value. When children contains a token chunk, it reads and
// clears every chunk of the session and returns the stored token bytes as one
// value.
func (a *sessionAudit) clearChildren(tr fdb.Transaction, sessionID uuid.UUID, children []fdb.KeyValue) (json.RawMessage, error) {
	tokensFound := false
	for _, child := range children {
		unpacked, err := tuple.Unpack(stripPrefix(child.Key))
		if err != nil || len(unpacked) <= childIdentityPosition {
			return nil, fmt.Errorf("decode child key %x of session %s", child.Key, sessionID)
		}
		part, _ := unpacked[childPartPosition].(string)
		if part == sessionTokenPart {
			tokensFound = true
			continue
		}
		if err := a.recordChildDelete(sessionID, part, unpacked[childIdentityPosition], child.Value); err != nil {
			return nil, err
		}
		tr.Clear(child.Key)
	}
	if !tokensFound {
		return nil, nil
	}
	return clearSessionTokens(tr, sessionID)
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

// clearSessionTokens reads and clears every token chunk of the session in one
// range. The session header bounds the chunk count.
func clearSessionTokens(tr fdb.Transaction, sessionID uuid.UUID) (json.RawMessage, error) {
	keyRange, err := fdb.PrefixRange(searchSessionPartPrefix(sessionID, sessionTokenPart))
	if err != nil {
		return nil, sessionStepError{operation: "create token range", err: err}
	}
	chunks, err := tr.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		return nil, sessionStepError{operation: "read query tokens", err: err}
	}
	var tokens []byte
	for _, chunk := range chunks {
		tokens = append(tokens, chunk.Value...)
	}
	tr.ClearRange(keyRange)
	return tokens, nil
}
