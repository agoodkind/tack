package foundationdb

// FoundationDB stores product entities with generic key patterns. It does
// not use separate assignment, label, or comment key families. Keys use the
// tuple layer. Organization-scoped keys place orgID early in the tuple to
// group each tenant's records.
const (
	// keyNodeInstance stores primary node records.
	//
	// (node_instance, orgID, nodeType, nodeID) -> Node JSON
	keyNodeInstance = "node_instance"

	// keyNodeView stores materialized read views with the same key structure as
	// node_instance.
	//
	// (node_view, orgID, nodeType, nodeID) -> NodeView JSON
	keyNodeView = "node_view"

	// keyNodeResolve stores global resolution records by nodeID without an
	// organization scope. Callers can resolve an entity without already knowing
	// its organization.
	//
	// (node_resolve, nodeID) -> NodeResolve JSON
	keyNodeResolve = "node_resolve"

	// keyNodeByProperty indexes properties with indexed PropertyDefs. It orders
	// entries by encoded value for filtered range scans.
	//
	// (node_by_property, orgID, nodeType, propName, encodedValue, nodeID) -> nil
	keyNodeByProperty = "node_by_property"

	// keyNodeReference indexes forward node references and enforces their
	// uniqueness.
	//
	// (node_reference, orgID, templateName, encoded) -> nodeID
	keyNodeReference = "node_reference"

	// keyNodeReferenceOwned indexes the references that each node owns.
	//
	// (node_reference_owned, orgID, nodeID, templateName) -> encoded
	keyNodeReferenceOwned = "node_reference_owned"

	// keyRelationship stores forward relationships.
	//
	// (relationship, orgID, sourceID, relationType, targetID) -> metadata JSON
	keyRelationship = "relationship"

	// keyRelationshipReverse indexes relationships by target.
	//
	// (relationship_reverse, orgID, targetID, relationType, sourceID) -> nil
	keyRelationshipReverse = "relationship_reverse"

	// keySequence stores atomic sequence counters for orgID, scopeNodeID, and
	// nodeType. scopeNodeID identifies the container that defines uniqueness,
	// typically a project. AllocateSequenceByKey uses counterKey instead of
	// scopeNodeID and nodeType.
	//
	// (sequence, orgID, scopeNodeID, nodeType) -> int64
	keySequence = "sequence"

	// keyNodeTypeDef stores NodeType configuration records.
	//
	// (node_type_def, orgID, typeID) -> NodeType JSON
	keyNodeTypeDef = "node_type_def"

	// keyPropertyDef stores PropertyDef records.
	//
	// (property_def, orgID, defID) -> PropertyDef JSON
	keyPropertyDef = "property_def"

	// The metadata lookup indexes map a type key or property name to record
	// IDs. The transaction that writes a record also writes its index entry.
	//
	// (node_type_by_key, orgID, typeKey, typeID) -> nil
	// (property_def_by_name, orgID, name, defID) -> nil
	keyNodeTypeByKey     = "node_type_by_key"
	keyPropertyDefByName = "property_def_by_name"

	// keyIdempotency indexes idempotency keys with IdempotencyRecord JSON
	// values. Older records may contain only raw nodeID bytes.
	//
	// (idempotency_key, orgID, key) -> IdempotencyRecord JSON
	keyIdempotency = "idempotency_key"

	// keyOpsOutbox stores audit outbox entries without an organization scope.
	// The relay reads entries from every organization in commit order. Entries
	// contain plain event JSON or multipart transport records.
	//
	// (ops_outbox, versionstamp) -> audit event JSON
	keyOpsOutbox = "ops_outbox"

	// The strict validation keys store the global activation record and write
	// fence bucket epochs. Neither key family uses an organization scope.
	//
	// (strict_activation) -> StrictActivation JSON
	// (write_fence, bucket) -> little-endian int64 epoch that atomic adds increment
	keyStrictActivation = "strict_activation"
	keyWriteFence       = "write_fence"

	// The relay lease key family stores the owner ID and expiry in FoundationDB
	// read versions.
	//
	// (ops_outbox_relay_lease) -> relay lease record JSON
	keyOpsOutboxRelayLease = "ops_outbox_relay_lease"

	// keyAuditReadPending stores pending reads by eventID. These key families
	// do not include orgID. bucket uses the first SHA-256 byte of the event ID
	// modulo 64. deadline and published use Unix nanoseconds.
	//
	// (audit_read_pending, eventID) -> pending read record JSON
	// (audit_read_expiry, bucket, deadline, eventID) -> nil
	// (audit_read_receipt, eventID) -> terminal receipt JSON
	// (audit_read_cleanup, bucket, published, eventID) -> nil
	keyAuditReadPending = "audit_read_pending"
	keyAuditReadExpiry  = "audit_read_expiry"
	keyAuditReadReceipt = "audit_read_receipt"
	keyAuditReadCleanup = "audit_read_cleanup"

	// keyNodeDeleteJob stores subtree delete jobs without an organization
	// scope. A runner in any process can list unfinished jobs across
	// organizations.
	//
	// (node_delete_job, jobID) -> SubtreeDeleteJob JSON
	keyNodeDeleteJob = "node_delete_job"

	// The repair checkpoint family stores one checkpoint per repair ID and uses
	// only that ID in its key. After a repair deletes a node and its resolution
	// record, later runs cannot read the organization ID from that record.
	//
	// (repair_checkpoint, repairID) -> RepairCheckpoint JSON
	keyRepairCheckpoint = "repair_checkpoint"

	// The search keys store durable search work and search state. Work, age,
	// and claim keys include a bucket number that searchBucket computes from
	// orgID and nodeID. A claim reads the age keys of one bucket at a time.
	//
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
	// (search_pending_write, class, orgID, nodeID, documentID, index) ->
	// pending page write
	//   record JSON
	// (search_exclusion, orgID, nodeID) -> exclusion record JSON of a node
	//   that search does not index
	// (search_scan, orgID) -> rescan generation counter
	// (search_projection, orgID) stores the 32-byte sum of definition
	//   projection hashes.
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
	keySearchPendingWrite      = "search_pending_write"
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
