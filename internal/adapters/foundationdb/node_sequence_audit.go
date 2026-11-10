package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

// sequenceTargetPrefix starts the URI of every sequence counter audit target.
const sequenceTargetPrefix = "tack:sequence/"

// sequenceRecord is the recorded old or new state of one sequence counter.
type sequenceRecord struct {
	Value int64 `json:"value"`
}

// sequenceChange returns the counter value to store for the current value
// and reports whether the counter changes.
type sequenceChange func(current int64) (int64, bool, error)

// scopeSequenceTarget returns the audit target of the counter that numbers
// nodes of nodeType under scopeNodeID in orgID.
func scopeSequenceTarget(orgID, scopeNodeID uuid.UUID, nodeType string) audit.Target {
	return sequenceTarget(sequenceTargetPrefix+orgID.String()+"/"+scopeNodeID.String()+"/"+nodeType, nodeType)
}

// keySequenceTarget returns the audit target of the counter stored under
// counterKey in orgID.
func keySequenceTarget(orgID uuid.UUID, counterKey string) audit.Target {
	return sequenceTarget(sequenceTargetPrefix+orgID.String()+"/"+counterKey, counterKey)
}

// sequenceTarget returns a resource target for the counter at uri. Its ID is
// the name-based UUID of uri.
func sequenceTarget(uri, name string) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindResource, NodeType: audit.TargetSequenceCounter, Reference: "", Name: name,
		URI: uri, RelationType: "", ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(uri)),
		SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// writeAuditedSequence reads the counter at key in tr and applies change.
// When change reports a new value, it writes the value and records one change
// event with the old and new values in tr. It returns the stored value and
// whether the counter changed.
func writeAuditedSequence(
	ctx context.Context, tr fdb.Transaction, changes *mutationAudit, key fdb.Key, target audit.Target, change sequenceChange,
) (int64, bool, error) {
	changes.startAttempt()
	raw, err := tr.Get(key).Get()
	if err != nil {
		wrapped := fmt.Errorf("read sequence %s: %w", target.URI, err)
		telemetry.L(ctx).ErrorContext(ctx, "node.sequence.read_failed", slog.String("uri", target.URI), slog.String("err", wrapped.Error()))
		return 0, false, loggedSearchError{err: wrapped}
	}
	current, err := readEncodedSequence(raw)
	if err != nil {
		return 0, false, err
	}
	next, changed, err := change(current)
	if err != nil || !changed {
		return current, false, err
	}
	if err := writeSequence(tr, key, next); err != nil {
		return 0, false, err
	}
	var before []byte
	if raw != nil {
		before, err = json.Marshal(sequenceRecord{Value: current})
		if err != nil {
			return 0, false, nodeOperationFailure(ctx, "encode the old value of "+target.URI, err)
		}
	}
	after, err := json.Marshal(sequenceRecord{Value: next})
	if err != nil {
		return 0, false, nodeOperationFailure(ctx, "encode the new value of "+target.URI, err)
	}
	if err := recordMetadataChange(ctx, tr, changes, target, before, after); err != nil {
		return 0, false, err
	}
	return next, true, nil
}

// incrementSequence adds one to the counter.
func incrementSequence(current int64) (int64, bool, error) {
	if current == math.MaxInt64 {
		return 0, false, fmt.Errorf("sequence value %d overflows after increment 1", current)
	}
	return current + 1, true, nil
}

// raiseSequence returns a change that raises the counter to value and never
// lowers it.
func raiseSequence(value int64) sequenceChange {
	return func(current int64) (int64, bool, error) {
		if current >= value {
			return current, false, nil
		}
		return value, true, nil
	}
}
