package node

import (
	"bytes"
	"errors"
	"hash/fnv"
	"slices"
	"time"

	"github.com/google/uuid"
)

// WriteFenceBuckets is the number of write fence buckets. Every product
// mutation atomically increments its node's bucket epoch inside the mutation
// transaction.
const WriteFenceBuckets = 64

// WriteFenceBucket returns the node's write fence bucket.
//
// The hash includes the organization ID followed by the node ID.
//
//   - The result is a bucket index below [WriteFenceBuckets].
func WriteFenceBucket(orgID, nodeID uuid.UUID) int {
	hash := fnv.New32a()
	_, _ = hash.Write(orgID[:])
	_, _ = hash.Write(nodeID[:])

	return int(hash.Sum32() % WriteFenceBuckets)
}

// ErrStrictEvidenceChanged reports that a bucket epoch or metadata epoch
// changed after the strict validation scan captured it.
var ErrStrictEvidenceChanged = errors.New("strict validation scan evidence changed")

// StrictEvidence stores the epochs that a strict validation scan captures
// before reading node records. The Buckets field stores every write fence
// bucket epoch. MetadataEpochs maps each organization with a stored metadata
// epoch to that epoch.
type StrictEvidence struct {
	Buckets        []int64
	MetadataEpochs map[uuid.UUID]int64
}

// StrictEvidenceChange lists the buckets and organizations with epoch changes
// between two captures.
type StrictEvidenceChange struct {
	Buckets       []int
	Organizations []uuid.UUID
}

// Empty reports whether the two captures contain identical epochs.
//
// The result depends on both the bucket changes and organization changes.
//
//   - An empty Buckets slice and an empty Organizations slice return true.
//   - Either slice containing an entry returns false.
func (c StrictEvidenceChange) Empty() bool {
	return len(c.Buckets) == 0 && len(c.Organizations) == 0
}

// Compare compares e from before the scan with the later evidence capture.
//
// The result lists each bucket and organization with a different epoch.
//
//   - A missing bucket or organization epoch counts as zero.
//   - Bucket changes follow bucket order.
//   - Organization changes sort by UUID bytes.
func (e StrictEvidence) Compare(later StrictEvidence) StrictEvidenceChange {
	change := StrictEvidenceChange{Buckets: nil, Organizations: nil}
	for bucket := range max(len(e.Buckets), len(later.Buckets)) {
		if evidenceAt(e.Buckets, bucket) != evidenceAt(later.Buckets, bucket) {
			change.Buckets = append(change.Buckets, bucket)
		}
	}

	for orgID, epoch := range e.MetadataEpochs {
		if later.MetadataEpochs[orgID] != epoch {
			change.Organizations = append(change.Organizations, orgID)
		}
	}

	for orgID, epoch := range later.MetadataEpochs {
		if _, known := e.MetadataEpochs[orgID]; !known && epoch != 0 {
			change.Organizations = append(change.Organizations, orgID)
		}
	}

	slices.SortFunc(
		change.Organizations,
		func(left, right uuid.UUID) int { return bytes.Compare(left[:], right[:]) },
	)

	return change
}

func evidenceAt(values []int64, position int) int64 {
	if position >= len(values) {
		return 0
	}

	return values[position]
}

// StrictRecord is one node record that a strict validation scan reads, with
// the graph that the scan read in the same transaction.
type StrictRecord struct {
	Node  *Node
	Graph ResultGraph
}

// StrictActivation is the stored strict validation activation record.
type StrictActivation struct {
	Active           bool      `json:"active"`
	ActivatedAt      time.Time `json:"activated_at"`
	ScanID           uuid.UUID `json:"scan_id"`
	RecordsValidated int       `json:"records_validated"`
}
