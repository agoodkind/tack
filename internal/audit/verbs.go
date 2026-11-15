package audit

import "strings"

// Verb identifies the recorded operation. Add new MCP-triggered verbs to
// ToolVerb as well. Coverage tests require a verb for each listed tool and an
// emitter for each declared verb. Stateless MCP does not record session
// boundaries (TACK-340).
type Verb string

const (
	// State changes must record synchronously without dropping events.
	VerbNodeCreate         Verb = "node.create"
	VerbNodeUpdate         Verb = "node.update"
	VerbNodeDelete         Verb = "node.delete"
	VerbRelationshipAdd    Verb = "relationship.add"
	VerbRelationshipRemove Verb = "relationship.remove"
	// VerbRelationshipUpdate records replacement of an existing relationship value.
	VerbRelationshipUpdate Verb = "relationship.update"
	VerbPropertyDefCreate  Verb = "property_def.create"
	VerbPropertyDefUpdate  Verb = "property_def.update"
	VerbPropertyDefDelete  Verb = "property_def.delete"
	VerbNodeTypeCreate     Verb = "node_type.create"
	VerbNodeTypeUpdate     Verb = "node_type.update"
	// VerbNodeTypeDelete records deletion of a node type definition.
	VerbNodeTypeDelete Verb = "node_type.delete"
	// VerbSequenceCreate records the first stored sequence counter value.
	VerbSequenceCreate Verb = "sequence.create"
	// VerbSequenceUpdate records a sequence counter value change.
	VerbSequenceUpdate Verb = "sequence.update"
	// VerbNodeReferenceRename records a node reference change.
	VerbNodeReferenceRename Verb = "node.reference_rename"
	VerbOrgMemberAdd        Verb = "org.member_add"
	VerbOrgMemberRemove     Verb = "org.member_remove"
	VerbOrgMemberRoleSet    Verb = "org.member_role_set"
	// VerbUserCreate records creation of an authenticated user.
	VerbUserCreate Verb = "user.create"

	// Auth verbs record authentication operations, including failures.
	VerbAuthLoginSucceeded Verb = "auth.login_succeeded"
	VerbAuthLoginFailed    Verb = "auth.login_failed"
	VerbAuthTokenCreate    Verb = "auth.token_create"
	VerbAuthTokenRevoke    Verb = "auth.token_revoke"
	VerbAuthTokenUsed      Verb = "auth.token_used"
	VerbAuthTokenRejected  Verb = "auth.token_rejected"

	// Read-class verbs support batched recording with a write-ahead log.
	VerbNodeRead          Verb = "node.read"
	VerbNodeList          Verb = "node.list"
	VerbNodeSearch        Verb = "node.search"
	VerbWorkspaceList     Verb = "workspace.list"
	VerbWorkspaceDescribe Verb = "workspace.describe"
	VerbMembersList       Verb = "members.list"
	VerbPropertyDefsList  Verb = "property_def.list"
	VerbPropsRead         Verb = "props.read"
	VerbRelationshipList  Verb = "relationship.list"
	VerbAuditRead         Verb = "audit.read"
	VerbAuditExport       Verb = "audit.export"

	// VerbMCPToolInvoked records calls independently of their domain changes,
	// including tools without a semantic verb mapping. The other transport verbs
	// record resource reads and prompt calls independently of domain changes.
	VerbMCPToolInvoked   Verb = "mcp.tool_invoked"
	VerbMCPResourceRead  Verb = "mcp.resource_read"
	VerbMCPPromptInvoked Verb = "mcp.prompt_invoked"

	// Audit verbs record operations on audit evidence itself.
	VerbAuditDropped       Verb = "audit.dropped"
	VerbAuditNotarized     Verb = "audit.notarized"
	VerbAuditPIIRedacted   Verb = "audit.pii_redacted"
	VerbAuditChainVerified Verb = "audit.chain_verified"

	// VerbServerServe records a server start and stop.
	VerbServerServe Verb = "server.serve"
	// VerbDatabaseMigrate records a database migration.
	VerbDatabaseMigrate Verb = "database.migrate"
	// VerbBootstrapSeed records an initial product seed.
	VerbBootstrapSeed Verb = "bootstrap.seed"
	// VerbAuditVerify records audit bundle verification.
	VerbAuditVerify Verb = "audit.verify"
	// VerbAuditSignersVerified records notarization checks against the valid
	// signer set.
	VerbAuditSignersVerified Verb = "audit.signers_verified"
	// VerbAuditKeyGenerate records audit signing key generation.
	VerbAuditKeyGenerate Verb = "audit.key_generate"
	// VerbAuditRolesSeed records audit role creation or rotation.
	VerbAuditRolesSeed Verb = "audit.roles_seed"
	// VerbOpsInspectRead records a node inspection read.
	VerbOpsInspectRead Verb = "ops.inspect_read"
	// VerbOpsInspectFind records an inspection lookup.
	VerbOpsInspectFind Verb = "ops.inspect_find"
	// VerbOpsInspectQuery records an inspection query.
	VerbOpsInspectQuery Verb = "ops.inspect_query"
	// VerbOpsVerifyNode records a node verification.
	VerbOpsVerifyNode Verb = "ops.verify_node"
	// VerbOpsValidateNode records repair validation.
	VerbOpsValidateNode Verb = "ops.validate_node"
	// VerbOpsRepairClasses records a repair class listing.
	VerbOpsRepairClasses Verb = "ops.repair_classes"
	// VerbOpsRepairPreview records a repair preview.
	VerbOpsRepairPreview Verb = "ops.repair_preview"
	// VerbOpsRepairApply records a repair application.
	VerbOpsRepairApply Verb = "ops.repair_apply"
	// VerbOpsRepairReferenceUniqueness records reference uniqueness repair work.
	VerbOpsRepairReferenceUniqueness Verb = "ops.repair_reference_uniqueness"
	// VerbOpsDatagenSeed records QA data generation.
	VerbOpsDatagenSeed Verb = "ops.datagen_seed"
	// VerbOpsDatagenSoak records QA traffic generation.
	VerbOpsDatagenSoak Verb = "ops.datagen_soak"
	// VerbOpsDatagenSearch records QA verification of public search.
	VerbOpsDatagenSearch Verb = "ops.datagen_search"
	// VerbOpsDatagenRepair records QA node integrity repair checks with
	// ops qa datagen repair and ops.datagen_repair.
	VerbOpsDatagenRepair Verb = "ops.datagen_repair"
	// VerbOpsDatagenLegacyLedgerRow records generation of historical ledger rows
	// without stored outcomes.
	VerbOpsDatagenLegacyLedgerRow Verb = "ops.datagen_legacy_ledger_row"
	// VerbOpsProvision records environment provisioning.
	VerbOpsProvision Verb = "ops.provision"
	// VerbOpsAuditDLQInspect records a dead-letter listing.
	VerbOpsAuditDLQInspect Verb = "ops.audit_dlq_inspect"
	// VerbOpsAuditDLQReplay records dead-letter republication to the audit topic.
	VerbOpsAuditDLQReplay Verb = "ops.audit_dlq_replay"
	// VerbOpsAuditConsumerOffsets records committed consumer lag and outbox
	// remainders.
	VerbOpsAuditConsumerOffsets Verb = "ops.audit_consumer_offsets"
	VerbOpsSearchAccessRollback Verb = "ops.search_access_rollback" // VerbOpsSearchAccessRollback records an access policy rollback.
	// VerbOpsAuditSchemaGuardProof records an engine-superuser CREATE TABLE probe
	// against the audit schema, including intent and outcome.
	VerbOpsAuditSchemaGuardProof Verb = "ops.audit_schema_guard_proof"
	// VerbOpsDBBreakGlass records one production SQL statement using the
	// required audited operator command (TACK-327).
	VerbOpsDBBreakGlass Verb = "ops.db_break_glass"
	// VerbOpsAuthTokenCreate records API token creation for an existing user.
	VerbOpsAuthTokenCreate Verb = "ops.auth_token_create"
	// VerbOpsAuthTokenList records a user's API token listing.
	VerbOpsAuthTokenList Verb = "ops.auth_token_list"
	// VerbOpsAuthTokenRevoke records API token revocation.
	VerbOpsAuthTokenRevoke Verb = "ops.auth_token_revoke"
	// VerbOpsActAsCreate records node creation as a specified user (TACK-424).
	VerbOpsActAsCreate Verb = "ops.act_as_create"
	// VerbOpsActAsGrant records the user, org, reason, and grant ID for operator
	// actions as a user. The user's own row includes that grant ID (TACK-424).
	VerbOpsActAsGrant Verb = "ops.act_as_grant"
	// VerbOpsBackfillDefaultChildren records default child backfills.
	VerbOpsBackfillDefaultChildren Verb = "ops.backfill_default_children"
	// VerbOpsBackfillSearchProjections records the one-time explicit metadata rollout.
	VerbOpsBackfillSearchProjections Verb = "ops.backfill_search_projections"
	// VerbOpsBackfillFixedDefinitions records the ops backfill
	// once-fixed-definitions command.
	VerbOpsBackfillFixedDefinitions Verb = "ops.backfill_fixed_definitions"
	// VerbOpsBackfillMetadataNameIndex records the node type-key and
	// property-name index backfill.
	VerbOpsBackfillMetadataNameIndex Verb = "ops.backfill_metadata_name_index"
	// VerbOpsBackfillOrphanNodes records deletion of parentless nodes and descendants.
	VerbOpsBackfillOrphanNodes Verb = "ops.backfill_orphan_nodes"
	// VerbOpsBackfillNodeIntegrity records the frozen-manifest integrity backfill
	// (TACK-582).
	VerbOpsBackfillNodeIntegrity Verb = "ops.backfill_node_integrity"
	// VerbOpsBackfillAuditPartitionNames records renaming audit.events children
	// outside the events_pYYYY_MM_DD format.
	VerbOpsBackfillAuditPartitionNames Verb = "ops.backfill_audit_partition_names"
	// VerbOpsBackfillDropKilltestSchema records the one-time killtest schema deletion.
	VerbOpsBackfillDropKilltestSchema Verb = "ops.backfill_drop_killtest_schema"
	// VerbOpsReindex records property index backfills.
	VerbOpsReindex Verb = "ops.reindex"
	// VerbOpsStrictValidation records strict validation scans and activation.
	VerbOpsStrictValidation Verb = "ops.strict_validation"
	// VerbOpsReferenceDuplicates records duplicate reference reports.
	VerbOpsReferenceDuplicates Verb = "ops.reference_duplicates"
	// VerbOpsBackup records a backup command without a subcommand.
	VerbOpsBackup Verb = "ops.backup"
	// VerbOpsBackupBucketsInit records backup bucket initialization.
	VerbOpsBackupBucketsInit Verb = "ops.backup_buckets_init"
	// VerbOpsBackupYBPITRInit records point-in-time recovery initialization.
	VerbOpsBackupYBPITRInit Verb = "ops.backup_yb_pitr_init"
	// VerbOpsBackupYBSnapshotExport records YugabyteDB snapshot export.
	VerbOpsBackupYBSnapshotExport Verb = "ops.backup_yb_snapshot_export"
	// VerbOpsBackupYBArchiveNode records a per-node YugabyteDB tablet archive.
	VerbOpsBackupYBArchiveNode Verb = "ops.backup_yb_archive_node"
	// VerbOpsBackupRestoreDrill records a backup restore drill.
	VerbOpsBackupRestoreDrill Verb = "ops.backup_restore_drill"
	// VerbOpsBackupFDBContinuousInit records FoundationDB continuous backup setup.
	VerbOpsBackupFDBContinuousInit Verb = "ops.backup_fdb_continuous_init"
	// VerbOpsBackupStalenessCheck records a backup staleness report.
	VerbOpsBackupStalenessCheck Verb = "ops.backup_staleness_check"
	// VerbOpsBackupFDBRestoreVersion records conversion of a time into an FDB
	// restore version.
	VerbOpsBackupFDBRestoreVersion Verb = "ops.backup_fdb_restore_version"
	// VerbOpsDeployVerify records checks of running images against deployed image
	// references.
	VerbOpsDeployVerify Verb = "ops.deploy_verify"
	// VerbOpsSearchProvision records search model, empty index, and alias creation.
	VerbOpsSearchProvision Verb = "ops.search_provision"
	// VerbOpsSearchVerify records search model, mapping, topology, alias, and
	// health checks.
	VerbOpsSearchVerify Verb = "ops.search_verify"
	// VerbOpsSearchSettings records persistent, transient, default, and effective
	// OpenSearch point-in-time limits.
	VerbOpsSearchSettings Verb = "ops.search_settings"
	// VerbOpsSearchAccessRollout records access policy rollout for one permission
	// authority.
	VerbOpsSearchAccessRollout Verb = "ops.search_access_rollout"
	// VerbOpsSearchReindex records search index replacement starts and resumptions.
	VerbOpsSearchReindex Verb = "ops.search_reindex"
	// VerbOpsLedgerNodePrepare records saved master-list alignment before restart
	// (TACK-489).
	VerbOpsLedgerNodePrepare Verb = "ops.ledger_node_prepare"
	// VerbOpsLedgerNodeWait records waiting for restarted-node replication
	// progress (TACK-471).
	VerbOpsLedgerNodeWait Verb = "ops.ledger_node_wait"
	// VerbOpsLedgerAuditBootstrap records migrations and audit role seeding that
	// create the outbox before other operator commands can record.
	VerbOpsLedgerAuditBootstrap Verb = "ops.ledger_audit_bootstrap"
	// VerbOpsLedgerBootstrapWait records waiting for expected masters, tablet
	// servers, and replicas during ledger bootstrap.
	VerbOpsLedgerBootstrapWait Verb = "ops.ledger_bootstrap_wait"
	// VerbOpsStoreStatus records product-store cluster status reads (TACK-408).
	VerbOpsStoreStatus Verb = "ops.store_status"
	// VerbOpsStoreSetRedundancy records a product-store copy-count change
	// (TACK-408).
	VerbOpsStoreSetRedundancy Verb = "ops.store_set_redundancy"
	// VerbOpsStoreSetCoordinators records coordinator-list changes (TACK-408).
	VerbOpsStoreSetCoordinators Verb = "ops.store_set_coordinators"
	// VerbOpsStoreExclude records process exclusion after data replication (TACK-408).
	VerbOpsStoreExclude Verb = "ops.store_exclude"
	// VerbSearchIndexStructure records index creation, splits, deletion, or alias
	// switches.
	VerbSearchIndexStructure Verb = "search.index_structure"
	// VerbSearchAccessRolloutPhase records a worker step that can change rollout
	// phase.
	VerbSearchAccessRolloutPhase Verb = "search.access_rollout_phase"
	// VerbSearchModelRepair records redeployment of a stuck serving model.
	VerbSearchModelRepair Verb = "search.model_repair"
	// VerbSearchSessionCreate records new search sessions, replays, or visited
	// records.
	VerbSearchSessionCreate Verb = "search.session_create"
	// VerbSearchSessionUpdate records search session record changes.
	VerbSearchSessionUpdate Verb = "search.session_update"
	// VerbSearchSessionDelete records removal of search sessions, replays, or
	// visited records.
	VerbSearchSessionDelete Verb = "search.session_delete"
	// VerbSearchPointInTimeCreate records an OpenSearch point-in-time creation.
	VerbSearchPointInTimeCreate Verb = "search.point_in_time_create"
	// VerbSearchPointInTimeDelete records an OpenSearch point-in-time deletion.
	VerbSearchPointInTimeDelete Verb = "search.point_in_time_delete"
	// VerbAuditPartitionMaintain records one consumer partition maintenance run.
	VerbAuditPartitionMaintain Verb = "audit.partition_maintain"
	// VerbNodeSubtreeDeleteResume records subtree deletion without an active
	// runner.
	VerbNodeSubtreeDeleteResume Verb = "node.subtree_delete_resume"
)

// stateChangeVerbs requires synchronous recording with product state changes.
// Operator commands declare their class in Spec.
var stateChangeVerbs = map[Verb]bool{
	VerbNodeCreate:          true,
	VerbNodeUpdate:          true,
	VerbNodeDelete:          true,
	VerbRelationshipAdd:     true,
	VerbRelationshipRemove:  true,
	VerbRelationshipUpdate:  true,
	VerbNodeTypeDelete:      true,
	VerbPropertyDefCreate:   true,
	VerbPropertyDefUpdate:   true,
	VerbPropertyDefDelete:   true,
	VerbNodeTypeCreate:      true,
	VerbNodeTypeUpdate:      true,
	VerbSequenceCreate:      true,
	VerbSequenceUpdate:      true,
	VerbOrgMemberAdd:        true,
	VerbOrgMemberRemove:     true,
	VerbOrgMemberRoleSet:    true,
	VerbUserCreate:          true,
	VerbAuthLoginSucceeded:  true,
	VerbAuthLoginFailed:     true,
	VerbAuthTokenCreate:     true,
	VerbAuthTokenRevoke:     true,
	VerbAuditPIIRedacted:    true,
	VerbSearchSessionCreate: true,
	VerbSearchSessionUpdate: true,
	VerbSearchSessionDelete: true,
}

func IsRead(v Verb) bool { return !stateChangeVerbs[v] }

// staticToolVerb maps fixed MCP tool names to nonempty verbs. ToolVerb matches
// per-NodeType tools by prefix. The service commits state-change events in the
// FoundationDB transaction (TACK-173); the wrapper records them only without a
// committed staged row and records read-class events itself.
var staticToolVerb = map[string]Verb{
	"tack_list_workspaces":     VerbWorkspaceList,
	"tack_list_members":        VerbMembersList,
	"tack_list_property_defs":  VerbPropertyDefsList,
	"tack_get_properties":      VerbPropsRead,
	"tack_add_relationship":    VerbRelationshipAdd,
	"tack_remove_relationship": VerbRelationshipRemove,
	"tack_list_relationships":  VerbRelationshipList,
	"tack_search":              VerbNodeSearch,
	"tack_getting_started":     VerbMCPPromptInvoked,
}

// perTypePrefixVerb maps NodeType-derived prefixes to verbs, longest prefixes
// first.
var perTypePrefixVerb = []struct {
	Prefix string
	Verb   Verb
}{
	{"tack_describe_", VerbWorkspaceDescribe}, // tack_describe_<entry-point-slug>
	{"tack_list_", VerbNodeList},              // tack_list_<plural>
	{"tack_create_", VerbNodeCreate},          // tack_create_<slug>
	{"tack_get_", VerbNodeRead},               // tack_get_<slug>
	{"tack_update_", VerbNodeUpdate},          // tack_update_<slug>
	{"tack_set_", VerbNodeUpdate},             // tack_set_<slug>_<property>
	{"tack_delete_", VerbNodeDelete},          // tack_delete_<slug>
}

// ToolVerb returns the registered tool's verb and coverage status. An empty
// verb with true coverage means another layer records the event; the wrapper
// must not record it again.
func ToolVerb(toolName string) (Verb, bool) {
	if v, ok := staticToolVerb[toolName]; ok {
		return v, true
	}

	for _, p := range perTypePrefixVerb {
		if strings.HasPrefix(toolName, p.Prefix) {
			return p.Verb, true
		}
	}

	return "", false
}
