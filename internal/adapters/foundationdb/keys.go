package foundationdb

import "bytes"

// FDB key space. Everything is expressed through one of a small set of generic
// patterns; nothing in the key space privileges a specific concept (no
// assignment keys, no label keys, no comment keys, etc.).
//
// All keys use the tuple layer. orgID sits early in the tuple for tenant
// locality.
const (
	// Primary node storage.
	// (node_instance, orgID, nodeType, nodeID) -> Node JSON
	keyNodeInstance = "node_instance"

	// Materialized read view. Mirrors node_instance key structure.
	// (node_view, orgID, nodeType, nodeID) -> NodeView JSON
	keyNodeView = "node_view"

	// Global resolution record; NOT org-scoped. Keyed by nodeID only so any
	// caller can resolve an entity without knowing its org upfront.
	// (node_resolve, nodeID) -> NodeResolve JSON
	keyNodeResolve = "node_resolve"

	// Secondary index for indexed PropertyDefs. Sorted by encoded value so
	// filtered range scans are cheap.
	// (node_by_property, orgID, nodeType, propName, encodedValue, nodeID) -> nil
	keyNodeByProperty = "node_by_property"

	// Forward node reference uniqueness index.
	// (node_reference, orgID, templateName, encoded) -> nodeID
	keyNodeReference = "node_reference"

	// Reverse node reference ownership index.
	// (node_reference_owned, orgID, nodeID, templateName) -> encoded
	keyNodeReferenceOwned = "node_reference_owned"

	// Forward relationship.
	// (relationship, orgID, sourceID, relationType, targetID) -> metadata JSON
	keyRelationship = "relationship"

	// Reverse relationship for lookups by target.
	// (relationship_reverse, orgID, targetID, relationType, sourceID) -> nil
	keyRelationshipReverse = "relationship_reverse"

	// Atomic sequence counters keyed by (orgID, scopeNodeID, nodeType).
	// scopeNodeID is the container that defines uniqueness (typically a project).
	// (sequence, orgID, scopeNodeID, nodeType) -> int64
	keySequence = "sequence"

	// NodeType configuration records.
	// (node_type_def, orgID, typeID) -> NodeType JSON
	keyNodeTypeDef = "node_type_def"

	// PropertyDef records.
	// (property_def, orgID, defID) -> PropertyDef JSON
	keyPropertyDef = "property_def"

	// The metadata lookup indexes map a type key or property name to record
	// IDs. The transaction that writes a record also writes its index entry.
	// (node_type_by_key, orgID, typeKey, typeID) -> nil
	// (property_def_by_name, orgID, name, defID) -> nil
	keyNodeTypeByKey     = "node_type_by_key"
	keyPropertyDefByName = "property_def_by_name"

	// Idempotency key index. The value is an IdempotencyRecord JSON payload.
	// Older records may contain only the raw nodeID bytes.
	// (idempotency_key, orgID, key) -> IdempotencyRecord JSON
	keyIdempotency = "idempotency_key"

	// Operator-command audit outbox. It is deliberately not org-scoped so the
	// relay can drain every org in commit order.
	// (ops_outbox, versionstamp) -> audit event JSON
	keyOpsOutbox = "ops_outbox"

	// Subtree delete jobs. The family is not org-scoped. A runner in any
	// process lists every unfinished job of every organization.
	// (node_delete_job, jobID) -> SubtreeDeleteJob JSON
	keyNodeDeleteJob = "node_delete_job"

	// The search keys store durable search work and search state. Work, age,
	// and claim keys include a bucket number that searchBucket computes from
	// (orgID, nodeID). A claim reads the age keys of one bucket at a time.
	// (search_generation, orgID, nodeID) -> external version counter
	// (search_revision, orgID, nodeID) -> generation of the last content change
	// (search_work, class, bucket, orgID, nodeID) -> pending work record JSON
	// (search_age, class, bucket, enqueuedUnixNano, orgID, nodeID) -> nil;
	//   each pending work record has one entry, sorted by enqueue time
	// (search_claim, class, bucket, orgID, nodeID) -> lease record JSON
	// (search_cursor, class, orgID, nodeID) -> progress record JSON; the
	//   rescan class stores its scan state here under the nil node ID
	// (search_issued, orgID, nodeID, revision, ordinal, projection) -> document ID
	// (search_access, orgID, nodeID) -> recorded access state JSON
	// (search_fanout, orgID, deletedNodeID, counterpartID) -> nil
	// (search_error, class, orgID, nodeID) -> last failure message
	// (search_scan, orgID) -> rescan generation counter
	// (search_projection, orgID) -> 32-byte sum of definition projection hashes
	// (search_index) -> serving physical index name
	// (search_epoch, orgID) -> metadata epoch counter; every property
	//   definition or node type change increments it
	// (search_rollout, authorityID) -> access policy rollout record JSON
	// (search_rollout_generation, authorityID) -> rollout work generation
	// (search_permission_event, authorityID) -> resource permission event counter
	// (search_rebuild) -> the one index replacement record JSON; its presence
	//   is the environment's replacement lease
	// (search_rebuild_generation) -> rebuild work generation
	// (search_restore_epoch) -> restore epoch that a restored replacement
	//   increments; every session binds it
	// (search_retired_since, index) -> Unix nanoseconds of the first page
	//   retirement in that physical index
	keySearchRebuild           = "search_rebuild"
	keySearchRebuildGeneration = "search_rebuild_generation"
	keySearchRestoreEpoch      = "search_restore_epoch"
	keySearchRetiredSince      = "search_retired_since"
	keySearchEpoch             = "search_epoch"
	keySearchRollout           = "search_rollout"
	keySearchRolloutGeneration = "search_rollout_generation"
	keySearchPermissionEvent   = "search_permission_event"
	keySearchGeneration        = "search_generation"
	keySearchRevision          = "search_revision"
	keySearchWork              = "search_work"
	keySearchAge               = "search_age"
	keySearchClaim             = "search_claim"
	keySearchCursor            = "search_cursor"
	keySearchIssued            = "search_issued"
	keySearchAccess            = "search_access"
	keySearchFanout            = "search_fanout"
	keySearchError             = "search_error"
	keySearchScan              = "search_scan"
	keySearchProjection        = "search_projection"
	keySearchIndex             = "search_index"
)

// testPrefix is prepended to every packed FDB key when non-nil. Production
// callers leave it nil; integration tests set a per-test UUID via
// SetTestPrefix so parallel tests on a shared FDB cluster cannot collide.
//
// Set only via SetTestPrefix from the test setup helper.
var testPrefix []byte

// SetTestPrefix configures a per-test key prefix. Pass nil to clear. The
// prefix is encoded as a single bytes element in the tuple layer so range
// scans stay tuple-aware.
//
// Tests own this. Do not call from production code.
func SetTestPrefix(prefix []byte) {
	testPrefix = prefix
}

// withPrefix prepends the active test prefix (if any) to a packed key. The
// production fast path returns the input unchanged.
func withPrefix(key []byte) []byte {
	if testPrefix == nil {
		return key
	}
	out := make([]byte, 0, len(testPrefix)+len(key))
	out = append(out, testPrefix...)
	out = append(out, key...)
	return out
}

// stripPrefix removes the active test prefix from a key returned by an FDB
// range read. Production callers leave testPrefix nil and the function is a
// no-op. Stores must call this before tuple.Unpack on a returned key so the
// unpack starts at the real tuple bytes rather than the raw test prefix.
func stripPrefix(key []byte) []byte {
	if testPrefix == nil {
		return key
	}
	if len(key) >= len(testPrefix) && bytes.Equal(key[:len(testPrefix)], testPrefix) {
		return key[len(testPrefix):]
	}
	return key
}

// TestPrefixRange returns a range covering every key under the active test
// prefix. Tests use this to range-clear all data they wrote during a run.
// Returns nil when no test prefix is set, so production code is safe to call.
func TestPrefixRange() []byte {
	if testPrefix == nil {
		return nil
	}
	return testPrefix
}
