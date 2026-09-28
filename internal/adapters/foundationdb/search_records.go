package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// searchWorkRecord is one pending work item. Every scheduling transaction
// rewrites each pending record of the node to the node's newest generation.
type searchWorkRecord struct {
	OrgID      uuid.UUID `json:"org_id"`
	NodeID     uuid.UUID `json:"node_id"`
	Generation int64     `json:"generation"`
	Revision   int64     `json:"revision"`
	Deleted    bool      `json:"deleted"`
	EnqueuedAt time.Time `json:"enqueued_at"`
}

// searchClaimRecord leases one work item. An empty owner marks a released
// item. Its retry waits until LeaseUntil. Mirror stores the replacement
// index or an empty string. When Mirror is not empty, the worker that owns
// the claim writes the claimed work to Target and to Mirror.
type searchClaimRecord struct {
	Owner      string    `json:"owner"`
	Generation int64     `json:"generation"`
	LeaseUntil time.Time `json:"lease_until"`
	Target     string    `json:"target"`
	Mirror     string    `json:"mirror,omitempty"`
}

// searchProgressRecord is the durable checkpoint of one class for one node.
type searchProgressRecord struct {
	Revision   int64  `json:"revision"`
	Generation int64  `json:"generation"`
	Projection string `json:"projection"`
	Cursor     string `json:"cursor"`
	Ordinal    uint64 `json:"ordinal"`
	Phase      string `json:"phase"`
}

// searchAccessRecord stores the compiled access of the node. Access work
// records it before it writes that access to any page document.
// PagesPending is true while some current page document can contain older
// access. DependentsPending is true while the nodes that derive their access
// from this node still need access work. Scheduling a newer generation of
// the pending work leaves both flags unchanged.
type searchAccessRecord struct {
	Access            node.SearchAccess `json:"access"`
	PagesPending      bool              `json:"pages_pending"`
	DependentsPending bool              `json:"dependents_pending"`
}

// searchScanRecord is the durable state of one organization rescan. Phase
// is scanDefinitions, scanTypes, or scanNodes. Stop is the node cursor at
// which the latest request arrived during the node pass. When the pass has
// read the last node, a nonempty Stop restarts the node pass at the first
// node, and that wrapped pass ends at Stop. Content and Access select the
// work each scanned node receives. The Names field limits content work to
// nodes with a value under one of the names. When Content is true and Names
// is empty, every scanned node receives content work.
type searchScanRecord struct {
	Phase   string   `json:"phase"`
	Cursor  string   `json:"cursor"`
	Stop    string   `json:"stop"`
	Wrapped bool     `json:"wrapped"`
	Content bool     `json:"content"`
	Names   []string `json:"names,omitempty"`
	Access  bool     `json:"access"`
}

type searchRecordValue interface {
	searchWorkRecord | searchClaimRecord | searchProgressRecord | searchAccessRecord | searchScanRecord | searchRolloutRecord |
		searchRebuildRecord
}

// readSearchRecord decodes the JSON record at key. It reports false when the
// key is absent.
func readSearchRecord[Record searchRecordValue](ctx context.Context, tr fdb.Transaction, key []byte, record *Record) (bool, error) {
	encoded, err := tr.Get(fdb.Key(key)).Get()
	if err != nil {
		return false, searchReadFailure(ctx, "read search record", err)
	}
	if len(encoded) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(encoded, record); err != nil {
		wrapped := fmt.Errorf("decode search record: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.record.decode_failed", slog.String("err", wrapped.Error()))
		return false, loggedSearchError{err: wrapped}
	}
	return true, nil
}

// writeSearchRecord encodes record as JSON at key.
func writeSearchRecord[Record searchRecordValue](ctx context.Context, tr fdb.Transaction, key []byte, record Record) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		wrapped := fmt.Errorf("encode search record: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.record.encode_failed", slog.String("err", wrapped.Error()))
		return loggedSearchError{err: wrapped}
	}
	tr.Set(fdb.Key(key), encoded)
	return nil
}

// readSearchCounter reads one tuple-encoded counter. An absent key reads zero.
func readSearchCounter(ctx context.Context, tr fdb.Transaction, key []byte) (int64, error) {
	encoded, err := tr.Get(fdb.Key(key)).Get()
	if err != nil {
		return 0, searchReadFailure(ctx, "read search counter", err)
	}
	if len(encoded) == 0 {
		return 0, nil
	}
	return unpackCounter(ctx, encoded)
}

// unpackCounter decodes one nonempty tuple-encoded counter value.
func unpackCounter(ctx context.Context, encoded []byte) (int64, error) {
	values, err := tuple.Unpack(encoded)
	if err != nil {
		return 0, searchReadFailure(ctx, "decode search counter", err)
	}
	if len(values) != 1 {
		return 0, searchReadFailure(ctx, "decode search counter", fmt.Errorf("counter has %d values", len(values)))
	}
	value, ok := values[0].(int64)
	if !ok || value < 0 {
		return 0, searchReadFailure(ctx, "decode search counter", fmt.Errorf("counter value %v is not a nonnegative integer", values[0]))
	}
	return value, nil
}

func writeSearchCounter(tr fdb.Transaction, key []byte, value int64) {
	tr.Set(fdb.Key(key), tuple.Tuple{value}.Pack())
}

// incrementSearchCounter reads, increments, and writes one counter.
func incrementSearchCounter(ctx context.Context, tr fdb.Transaction, key []byte) (int64, error) {
	current, err := readSearchCounter(ctx, tr, key)
	if err != nil {
		return 0, err
	}
	if current == math.MaxInt64 {
		wrapped := fmt.Errorf("search counter overflows at %d", current)
		telemetry.L(ctx).ErrorContext(ctx, "search.counter.overflow", slog.String("err", wrapped.Error()))
		return 0, loggedSearchError{err: wrapped}
	}
	writeSearchCounter(tr, key, current+1)
	return current + 1, nil
}
