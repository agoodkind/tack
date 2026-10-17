package foundationdb

import (
	"context"
	"slices"
	"strings"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
)

// repairRelatedSet collects related node IDs and reuses records from the
// ancestor traversal.
type repairRelatedSet struct {
	records map[uuid.UUID]*RepairNodeRecord
}

func newRepairRelatedSet() *repairRelatedSet {
	return &repairRelatedSet{records: map[uuid.UUID]*RepairNodeRecord{}}
}

// add requests nodeID only if the set does not already contain it.
func (set *repairRelatedSet) add(nodeID uuid.UUID) {
	if nodeID == uuid.Nil {
		return
	}
	if _, present := set.records[nodeID]; !present {
		set.records[nodeID] = nil
	}
}

// store saves a record that the caller has already read.
func (set *repairRelatedSet) store(record RepairNodeRecord) {
	set.records[record.NodeID] = &record
}

// read uses tr to read requested nodes that do not already have records.
// It returns all records sorted by node ID.
func (set *repairRelatedSet) read(ctx context.Context, tr fdb.Transaction) ([]RepairNodeRecord, error) {
	nodeIDs := make([]uuid.UUID, 0, len(set.records))
	for nodeID := range set.records {
		nodeIDs = append(nodeIDs, nodeID)
	}
	slices.SortFunc(nodeIDs, func(left, right uuid.UUID) int { return strings.Compare(left.String(), right.String()) })
	records := make([]RepairNodeRecord, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		known := set.records[nodeID]
		if known != nil {
			records = append(records, *known)
			continue
		}
		record, err := readRepairNode(ctx, tr, nodeID)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}
