package foundationdb

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// searchRolloutAffectedLimit is the largest set size that the affected pass
// verifies node by node. The rollout restarts the full authority scan for a
// larger set.
const searchRolloutAffectedLimit = 256

const (
	// rolloutAffectedMarker is the key element for affected set entries.
	rolloutAffectedMarker = "affected"
	// rolloutAffectedCountMarker is the key element for the affected set size
	// counter.
	rolloutAffectedCountMarker = "affected_count"
)

// rolloutAffectedPresent is the nonempty value that each affected set entry
// stores. A read distinguishes a present entry from an absent entry.
var rolloutAffectedPresent = []byte{1}

// rolloutAffectedIncrement is the atomic add operand for one in little
// endian byte order.
var rolloutAffectedIncrement = []byte{1, 0, 0, 0, 0, 0, 0, 0}

// rolloutAffectedDecrement is the atomic add operand for minus one in little
// endian byte order.
var rolloutAffectedDecrement = []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

func searchRolloutAffectedPrefix(authorityID uuid.UUID, generation int64) []byte {
	return withPrefix(tuple.Tuple{keySearchRollout, authorityID.String(), rolloutAffectedMarker, generation}.Pack())
}

func searchRolloutAffectedKey(authorityID uuid.UUID, generation int64, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchRollout, authorityID.String(), rolloutAffectedMarker, generation, nodeID.String()}.Pack())
}

func searchRolloutAffectedCountKey(authorityID uuid.UUID, generation int64) []byte {
	return withPrefix(tuple.Tuple{keySearchRollout, authorityID.String(), rolloutAffectedCountMarker, generation}.Pack())
}

// collectsAffected reports whether the rollout records affected nodes.
func collectsAffected(record searchRolloutRecord) bool {
	pending := record.Phase == string(searchdomain.AccessBackfill) || record.Phase == string(searchdomain.AccessVerifying)
	return pending && len(record.WriteVersions) == 2 && record.ActiveVersion != record.CandidateVersion
}

// recordRolloutAffected records one grant endpoint in the affected set
// within the transaction that schedules its access work.
func recordRolloutAffected(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) error {
	record, collecting, err := collectingRollout(ctx, tr, orgID)
	if err != nil || !collecting {
		return err
	}
	return addRolloutAffected(ctx, tr, record, nodeID)
}

// recordRolloutDependents records dependents in the affected set when the
// node that schedules them is in the set.
func recordRolloutDependents(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID, dependents []uuid.UUID) error {
	record, collecting, err := collectingRollout(ctx, tr, orgID)
	if err != nil || !collecting || len(dependents) == 0 {
		return err
	}
	present, err := tr.Get(fdb.Key(searchRolloutAffectedKey(orgID, record.Generation, nodeID))).Get()
	if err != nil {
		return searchReadFailure(ctx, "read affected node "+nodeID.String(), err)
	}
	if len(present) == 0 {
		return nil
	}
	for _, dependentID := range dependents {
		if err := addRolloutAffected(ctx, tr, record, dependentID); err != nil {
			return err
		}
	}
	return nil
}

// collectingRollout reads the rollout record with a snapshot read and
// reports whether it records affected nodes. The snapshot read does not add
// a read conflict to the grant transaction.
func collectingRollout(ctx context.Context, tr fdb.Transaction, orgID uuid.UUID) (searchRolloutRecord, bool, error) {
	var record searchRolloutRecord
	encoded, err := tr.Snapshot().Get(fdb.Key(searchRolloutKey(orgID))).Get()
	if err != nil {
		return record, false, searchReadFailure(ctx, "read access rollout of authority "+orgID.String(), err)
	}
	if len(encoded) == 0 {
		return record, false, nil
	}
	if err := json.Unmarshal(encoded, &record); err != nil {
		return record, false, searchReadFailure(ctx, "decode access rollout of authority "+orgID.String(), err)
	}
	return record, collectsAffected(record), nil
}

// addRolloutAffected adds one resolving node to the affected set and
// increments the counter. It skips nodes already in the set, nodes that no
// longer resolve, and every node once the counter exceeds the bound.
func addRolloutAffected(ctx context.Context, tr fdb.Transaction, record searchRolloutRecord, nodeID uuid.UUID) error {
	size, err := readRolloutAffectedCount(ctx, tr, record.AuthorityID, record.Generation)
	if err != nil || size > searchRolloutAffectedLimit {
		return err
	}
	key := fdb.Key(searchRolloutAffectedKey(record.AuthorityID, record.Generation, nodeID))
	present, err := tr.Get(key).Get()
	if err != nil {
		return searchReadFailure(ctx, "read affected node "+nodeID.String(), err)
	}
	if len(present) > 0 {
		return nil
	}
	resolved, err := tr.Get(fdb.Key(nodeResolveKey(nodeID))).Get()
	if err != nil {
		return searchReadFailure(ctx, "read node resolution "+nodeID.String()+" for an access rollout", err)
	}
	if len(resolved) == 0 {
		return nil
	}
	tr.Set(key, rolloutAffectedPresent)
	tr.Add(fdb.Key(searchRolloutAffectedCountKey(record.AuthorityID, record.Generation)), rolloutAffectedIncrement)
	return nil
}

// readRolloutAffectedCount reads the affected set size counter.
func readRolloutAffectedCount(ctx context.Context, tr fdb.Transaction, authorityID uuid.UUID, generation int64) (int64, error) {
	encoded, err := tr.Get(fdb.Key(searchRolloutAffectedCountKey(authorityID, generation))).Get()
	if err != nil {
		return 0, searchReadFailure(ctx, "read affected node count of authority "+authorityID.String(), err)
	}
	if len(encoded) == 0 {
		return 0, nil
	}
	if len(encoded) != 8 {
		return 0, searchReadFailure(ctx, "decode affected node count", fmt.Errorf("counter has %d bytes", len(encoded)))
	}
	value := binary.LittleEndian.Uint64(encoded)
	if value > math.MaxInt64 {
		return 0, searchReadFailure(ctx, "decode affected node count", fmt.Errorf("counter %d overflows", value))
	}
	return int64(value), nil
}

// firstRolloutAffected returns the first node in the affected set.
func firstRolloutAffected(ctx context.Context, tr fdb.Transaction, authorityID uuid.UUID, generation int64) (uuid.UUID, bool, error) {
	keyRange, err := fdb.PrefixRange(searchRolloutAffectedPrefix(authorityID, generation))
	if err != nil {
		return uuid.Nil, false, searchReadFailure(ctx, "create affected node range", err)
	}
	items, err := tr.GetRange(keyRange, fdb.RangeOptions{Limit: 1}).GetSliceWithError()
	if err != nil {
		return uuid.Nil, false, searchReadFailure(ctx, "read affected nodes of authority "+authorityID.String(), err)
	}
	if len(items) == 0 {
		return uuid.Nil, false, nil
	}
	values, err := tuple.Unpack(stripPrefix(items[0].Key))
	if err != nil {
		return uuid.Nil, false, searchReadFailure(ctx, "unpack affected node key", err)
	}
	nodeText, isText := values[len(values)-1].(string)
	nodeID, parseErr := uuid.Parse(nodeText)
	if !isText || parseErr != nil {
		return uuid.Nil, false, searchReadFailure(ctx, "decode affected node key", fmt.Errorf("affected key has no node identifier"))
	}
	return nodeID, true, nil
}

// removeRolloutAffected removes one node from the affected set and
// decrements the counter.
func removeRolloutAffected(tr fdb.Transaction, authorityID uuid.UUID, generation int64, nodeID uuid.UUID) {
	tr.Clear(fdb.Key(searchRolloutAffectedKey(authorityID, generation, nodeID)))
	tr.Add(fdb.Key(searchRolloutAffectedCountKey(authorityID, generation)), rolloutAffectedDecrement)
}

// clearRolloutAffected clears the affected set and its counter for one
// rollout generation.
func clearRolloutAffected(ctx context.Context, tr fdb.Transaction, authorityID uuid.UUID, generation int64) error {
	keyRange, err := fdb.PrefixRange(searchRolloutAffectedPrefix(authorityID, generation))
	if err != nil {
		return searchReadFailure(ctx, "create affected node range", err)
	}
	tr.ClearRange(keyRange)
	tr.Clear(fdb.Key(searchRolloutAffectedCountKey(authorityID, generation)))
	return nil
}
