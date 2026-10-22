package foundationdb

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// searchRolloutAffectedLimit is the largest affected set the affected pass
// verifies node by node. A larger set restarts the full verification pass.
const searchRolloutAffectedLimit = 256

const (
	// rolloutAffectedMarker is the key element for affected set entries.
	rolloutAffectedMarker = "affected"
	// rolloutAffectedCountMarker is the key element for the recording counter.
	rolloutAffectedCountMarker = "affected_count"
	// rolloutAffectedCollectMarker is the key element for the collection key.
	rolloutAffectedCollectMarker = "affected_collect"
)

// rolloutAffectedPresent is the stored value of an affected set entry.
var rolloutAffectedPresent = []byte{1}

// rolloutAffectedIncrement is the atomic add operand for one in little
// endian byte order.
var rolloutAffectedIncrement = []byte{1, 0, 0, 0, 0, 0, 0, 0}

func searchRolloutAffectedPrefix(authorityID uuid.UUID, generation int64) []byte {
	return withPrefix(tuple.Tuple{keySearchRollout, authorityID.String(), rolloutAffectedMarker, generation}.Pack())
}

func searchRolloutAffectedKey(authorityID uuid.UUID, generation int64, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchRollout, authorityID.String(), rolloutAffectedMarker, generation, nodeID.String()}.Pack())
}

func searchRolloutAffectedCountKey(authorityID uuid.UUID, generation int64) []byte {
	return withPrefix(tuple.Tuple{keySearchRollout, authorityID.String(), rolloutAffectedCountMarker, generation}.Pack())
}

func searchRolloutAffectedCollectKey(authorityID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchRollout, authorityID.String(), rolloutAffectedCollectMarker}.Pack())
}

// startRolloutCollecting writes the collection key with the rollout
// generation when the rollout enters verifying.
func startRolloutCollecting(tr fdb.Transaction, authorityID uuid.UUID, generation int64) {
	tr.Set(fdb.Key(searchRolloutAffectedCollectKey(authorityID)), tuple.Tuple{generation}.Pack())
}

// stopRolloutCollecting clears the collection key and one generation's
// affected set and recording counter.
func stopRolloutCollecting(ctx context.Context, tr fdb.Transaction, authorityID uuid.UUID, generation int64) error {
	tr.Clear(fdb.Key(searchRolloutAffectedCollectKey(authorityID)))
	return clearRolloutAffected(ctx, tr, authorityID, generation)
}

// recordRolloutAffected records one node in the affected set inside the
// transaction that schedules its access work while the collection key
// exists. It writes the entry without reading it and adds one to the
// recording counter with an atomic add without reading the counter.
func recordRolloutAffected(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) error {
	encoded, err := tr.Get(fdb.Key(searchRolloutAffectedCollectKey(orgID))).Get()
	if err != nil {
		return searchReadFailure(ctx, "read affected node collection of authority "+orgID.String(), err)
	}
	if len(encoded) == 0 {
		return nil
	}
	generation, err := unpackCounter(ctx, encoded)
	if err != nil {
		return err
	}
	tr.Set(fdb.Key(searchRolloutAffectedKey(orgID, generation, nodeID)), rolloutAffectedPresent)
	tr.Add(fdb.Key(searchRolloutAffectedCountKey(orgID, generation)), rolloutAffectedIncrement)
	return nil
}

// readRolloutAffectedCount reads the recording counter of one rollout
// generation.
func readRolloutAffectedCount(ctx context.Context, tr fdb.ReadTransaction, authorityID uuid.UUID, generation int64) (int64, error) {
	encoded, err := tr.Get(fdb.Key(searchRolloutAffectedCountKey(authorityID, generation))).Get()
	if err != nil {
		return 0, searchReadFailure(ctx, "read affected node count of authority "+authorityID.String(), err)
	}
	if len(encoded) == 0 {
		return 0, nil
	}
	if len(encoded) != 8 {
		return 0, searchReadFailure(ctx, "decode affected node count of authority "+authorityID.String(), fmt.Errorf("counter has %d bytes", len(encoded)))
	}
	value := binary.LittleEndian.Uint64(encoded)
	if value > math.MaxInt64 {
		return 0, searchReadFailure(ctx, "decode affected node count of authority "+authorityID.String(), fmt.Errorf("counter %d overflows", value))
	}
	return int64(value), nil
}

// readRolloutAffected reads at most limit nodes from the affected set of one
// rollout generation in key order.
func readRolloutAffected(ctx context.Context, tr fdb.ReadTransaction, authorityID uuid.UUID, generation int64, limit int) ([]uuid.UUID, error) {
	keyRange, err := fdb.PrefixRange(searchRolloutAffectedPrefix(authorityID, generation))
	if err != nil {
		return nil, searchReadFailure(ctx, "create affected node range of authority "+authorityID.String(), err)
	}
	items, err := tr.GetRange(keyRange, fdb.RangeOptions{Limit: limit}).GetSliceWithError()
	if err != nil {
		return nil, searchReadFailure(ctx, "read affected nodes of authority "+authorityID.String(), err)
	}
	nodes := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		values, unpackErr := tuple.Unpack(stripPrefix(item.Key))
		if unpackErr != nil {
			return nil, searchReadFailure(ctx, fmt.Sprintf("unpack affected node key %x of authority %s", item.Key, authorityID), unpackErr)
		}
		nodeText, isText := values[len(values)-1].(string)
		nodeID, parseErr := uuid.Parse(nodeText)
		if !isText || parseErr != nil {
			return nil, searchReadFailure(ctx, "decode affected node key of authority "+authorityID.String(),
				fmt.Errorf("affected key %x has no node identifier", item.Key))
		}
		nodes = append(nodes, nodeID)
	}
	return nodes, nil
}

// removeRolloutAffected removes one node from the affected set.
func removeRolloutAffected(tr fdb.Transaction, authorityID uuid.UUID, generation int64, nodeID uuid.UUID) {
	tr.Clear(fdb.Key(searchRolloutAffectedKey(authorityID, generation, nodeID)))
}

// clearRolloutAffected clears the affected set and recording counter of one
// rollout generation.
func clearRolloutAffected(ctx context.Context, tr fdb.Transaction, authorityID uuid.UUID, generation int64) error {
	keyRange, err := fdb.PrefixRange(searchRolloutAffectedPrefix(authorityID, generation))
	if err != nil {
		return searchReadFailure(ctx, "create affected node range of authority "+authorityID.String(), err)
	}
	tr.ClearRange(keyRange)
	tr.Clear(fdb.Key(searchRolloutAffectedCountKey(authorityID, generation)))
	return nil
}

// AffectedNodes reads the recording counter and at most 257 affected set
// entries for the current rollout generation of authorityID.
func (s *SearchAccessRolloutStore) AffectedNodes(ctx context.Context, authorityID uuid.UUID) (affected searchdomain.AffectedNodes, err error) {
	defer telemetry.FDBOp(ctx, "store.search_rollout.affected_nodes")(&err)
	err = transactSearch(ctx, s.db, func(tr fdb.Transaction) error {
		record, readErr := readRollout(ctx, tr, authorityID)
		if readErr != nil {
			return readErr
		}
		recorded, readErr := readRolloutAffectedCount(ctx, tr, authorityID, record.Generation)
		if readErr != nil {
			return readErr
		}
		nodes, readErr := readRolloutAffected(ctx, tr, authorityID, record.Generation, searchRolloutAffectedLimit+1)
		affected = searchdomain.AffectedNodes{Recorded: recorded, Nodes: nodes}
		return readErr
	})
	if err != nil {
		return affected, searchStorageError(ctx, "search.rollout.affected_read_failed", "read affected nodes of authority "+authorityID.String(), uuid.Nil, err)
	}
	return affected, nil
}
