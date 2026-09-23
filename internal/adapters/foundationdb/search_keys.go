package foundationdb

import (
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

func searchGenerationKey(orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchGeneration, orgID.String(), nodeID.String()}.Pack())
}

func searchRevisionKey(orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchRevision, orgID.String(), nodeID.String()}.Pack())
}

func searchWorkKey(class string, bucket int, orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchWork, class, bucket, orgID.String(), nodeID.String()}.Pack())
}

// searchAgeKey orders one pending work record by its enqueue time inside its
// class and bucket.
func searchAgeKey(class string, bucket int, enqueuedAt time.Time, orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchAge, class, bucket, enqueuedAt.UnixNano(), orgID.String(), nodeID.String()}.Pack())
}

func searchAgePrefix(class string, bucket int) []byte {
	return withPrefix(tuple.Tuple{keySearchAge, class, bucket}.Pack())
}

func searchClaimKey(class string, bucket int, orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchClaim, class, bucket, orgID.String(), nodeID.String()}.Pack())
}

func searchCursorKey(class string, orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchCursor, class, orgID.String(), nodeID.String()}.Pack())
}

func searchIssuedPrefix(orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchIssued, orgID.String(), nodeID.String()}.Pack())
}

func searchIssuedRevisionPrefix(orgID, nodeID uuid.UUID, revision int64) []byte {
	return withPrefix(tuple.Tuple{keySearchIssued, orgID.String(), nodeID.String(), revision}.Pack())
}

func searchIssuedOrdinalPrefix(orgID, nodeID uuid.UUID, revision int64, ordinal uint64) []byte {
	return withPrefix(tuple.Tuple{keySearchIssued, orgID.String(), nodeID.String(), revision, ordinal}.Pack())
}

// searchIssuedKey identifies one registered page by revision, ordinal, and
// projection version. Two owners that register the same ordinal of one
// revision under different projections record two document IDs.
func searchIssuedKey(orgID, nodeID uuid.UUID, revision int64, ordinal uint64, projection string) []byte {
	return withPrefix(tuple.Tuple{keySearchIssued, orgID.String(), nodeID.String(), revision, ordinal, projection}.Pack())
}

func searchAccessKey(orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchAccess, orgID.String(), nodeID.String()}.Pack())
}

func searchFanoutKey(orgID, deletedID, counterpartID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchFanout, orgID.String(), deletedID.String(), counterpartID.String()}.Pack())
}

func searchFanoutPrefix(orgID, deletedID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchFanout, orgID.String(), deletedID.String()}.Pack())
}

func searchErrorKey(class string, orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchError, class, orgID.String(), nodeID.String()}.Pack())
}

func searchScanKey(orgID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchScan, orgID.String()}.Pack())
}

func searchProjectionKey(orgID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchProjection, orgID.String()}.Pack())
}

func searchIndexKey() []byte {
	return withPrefix(tuple.Tuple{keySearchIndex}.Pack())
}
