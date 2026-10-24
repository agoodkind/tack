package foundationdb

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

	// The relay lease key family stores the owner ID and the expiry in
	// FoundationDB read versions.
	// (ops_outbox_relay_lease) -> relay lease record JSON
	keyOpsOutboxRelayLease = "ops_outbox_relay_lease"

	// keyAuditReadPending stores pending reads by eventID. These key families
	// do not include orgID. bucket uses the first SHA-256 byte of the event ID
	// modulo 64. deadline and published use Unix nanoseconds.
	// (audit_read_pending, eventID) -> pending read record JSON
	// (audit_read_expiry, bucket, deadline, eventID) -> nil
	// (audit_read_receipt, eventID) -> terminal receipt JSON
	// (audit_read_cleanup, bucket, published, eventID) -> nil
	keyAuditReadPending = "audit_read_pending"
	keyAuditReadExpiry  = "audit_read_expiry"
	keyAuditReadReceipt = "audit_read_receipt"
	keyAuditReadCleanup = "audit_read_cleanup"

	// Subtree delete jobs. The family is not org-scoped. A runner in any
	// process lists every unfinished job of every organization.
	// (node_delete_job, jobID) -> SubtreeDeleteJob JSON
	keyNodeDeleteJob = "node_delete_job"

	// The repair checkpoint family stores one checkpoint per repair ID and uses
	// only the repair ID in its key because later runs cannot read the organization
	// ID from a node's resolution record after a repair deletes the node and that
	// record.
	// (repair_checkpoint, repairID) -> RepairCheckpoint JSON
	keyRepairCheckpoint = "repair_checkpoint"

	// The search keys store durable search work and search state. Work, age,
	// and claim keys include a bucket number that searchBucket computes from
	// (orgID, nodeID). A claim reads the age keys of one bucket at a time.
	// (search_generation, orgID, nodeID) -> search generation counter
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
	// (search_attempt, class, orgID, nodeID) -> counted failures of the class
	//   since its last finished item
	// (search_exclusion, orgID, nodeID) -> exclusion record JSON of a node
	//   that search does not index
	// (search_scan, orgID) -> rescan generation counter
	// (search_projection, orgID) stores the 32-byte sum of definition
	//   projection hashes. Releases without a projection state record
	//   read this legacy digest.
	// (search_projection_state, orgID) stores the active projection
	//   state as JSON: renderer, format, digest, and activation.
	// (search_projection_sweep) stores the newest writer's sweep
	//   record as JSON: renderer, format, organization cursor, and
	//   completion state.
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
	// (search_model_repair) -> the one model repair record JSON: model ID,
	//   episode start, attempt count, and last attempt time
	keySearchRebuild           = "search_rebuild"
	keySearchRebuildGeneration = "search_rebuild_generation"
	keySearchRestoreEpoch      = "search_restore_epoch"
	keySearchRetiredSince      = "search_retired_since"
	keySearchModelRepair       = "search_model_repair"
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
	keySearchAttempt           = "search_attempt"
	keySearchExclusion         = "search_exclusion"
	keySearchScan              = "search_scan"
	keySearchProjection        = "search_projection"
	keySearchProjectionState   = "search_projection_state"
	keySearchProjectionSweep   = "search_projection_sweep"
	keySearchIndex             = "search_index"

	// (search_warning, orgID, nodeID, propertyName) -> one verified property
	//   diagnostic JSON of the node
	// (search_warning_revision, orgID, nodeID) -> content revision counter of
	//   the node's verified warning set
	// (search_warning_staged, class, orgID, nodeID, propertyName) -> one staged
	//   property diagnostic JSON for the claimed work class
	//   indexing stages the set from page ordinal 0 and promotes it to the
	//   verified set after the revision finishes indexing
	// (search_warning_staged_version, class, orgID, nodeID) -> JSON of the
	//   revision, generation, and projection of the staged set
	keySearchWarning              = "search_warning"
	keySearchWarningRevision      = "search_warning_revision"
	keySearchWarningStaged        = "search_warning_staged"
	keySearchWarningStagedVersion = "search_warning_staged_version"
)
