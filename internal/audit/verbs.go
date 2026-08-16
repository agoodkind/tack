package audit

import "strings"

// Verb is the stable identifier of what happened. New verbs must be added
// here and to ToolVerb (when MCP-triggerable). The coverage test in
// coverage_test.go fails the build if a registered MCP tool lacks a mapping.
type Verb string

const (
	// State changes are written synchronously, and they must not be dropped.
	VerbNodeCreate         Verb = "node.create"
	VerbNodeUpdate         Verb = "node.update"
	VerbNodeDelete         Verb = "node.delete"
	VerbRelationshipAdd    Verb = "relationship.add"
	VerbRelationshipRemove Verb = "relationship.remove"
	VerbPropertyDefCreate  Verb = "property_def.create"
	VerbPropertyDefUpdate  Verb = "property_def.update"
	VerbPropertyDefDelete  Verb = "property_def.delete"
	VerbNodeTypeCreate     Verb = "node_type.create"
	VerbNodeTypeUpdate     Verb = "node_type.update"
	// VerbNodeReferenceRename records a change to a node reference.
	VerbNodeReferenceRename Verb = "node.reference_rename"
	VerbOrgMemberAdd        Verb = "org.member_add"
	VerbOrgMemberRemove     Verb = "org.member_remove"
	VerbOrgMemberRoleSet    Verb = "org.member_role_set"
	// VerbUserCreate records creation of an authenticated user.
	VerbUserCreate Verb = "user.create"

	// Auth covers every breath, including failures.
	VerbAuthLoginSucceeded Verb = "auth.login_succeeded"
	VerbAuthLoginFailed    Verb = "auth.login_failed"
	VerbAuthTokenCreate    Verb = "auth.token_create"
	VerbAuthTokenRevoke    Verb = "auth.token_revoke"
	VerbAuthTokenUsed      Verb = "auth.token_used"
	VerbAuthTokenRejected  Verb = "auth.token_rejected"

	// Reads are batched via WAL for hot-path tolerance.
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

	// MCP transport plumbing.
	VerbMCPSessionStart  Verb = "mcp.session_start"
	VerbMCPSessionEnd    Verb = "mcp.session_end"
	VerbMCPToolInvoked   Verb = "mcp.tool_invoked"
	VerbMCPResourceRead  Verb = "mcp.resource_read"
	VerbMCPPromptInvoked Verb = "mcp.prompt_invoked"

	// Audit-of-audit.
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
	// VerbAuditKeyGenerate records audit signing key generation.
	VerbAuditKeyGenerate Verb = "audit.key_generate"
	// VerbAuditRolesSeed records audit role creation or rotation.
	VerbAuditRolesSeed Verb = "audit.roles_seed"
	// VerbOpsAuditReconstructReferenceRenames records ledger reconstruction.
	VerbOpsAuditReconstructReferenceRenames Verb = "ops.audit_reconstruct_reference_renames"
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
	// VerbOpsDatagenReferenceRepairShape records QA generation of the
	// pre-repair reference shape.
	VerbOpsDatagenReferenceRepairShape Verb = "ops.datagen_reference_repair_shape"
	// VerbOpsProvision records environment provisioning.
	VerbOpsProvision Verb = "ops.provision"
	// VerbOpsBackfillDefaultChildren records default child backfills.
	VerbOpsBackfillDefaultChildren Verb = "ops.backfill_default_children"
	// VerbOpsReindex records property index backfills.
	VerbOpsReindex Verb = "ops.reindex"
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
	// VerbOpsBackupRestoreDrill records a backup restore drill.
	VerbOpsBackupRestoreDrill Verb = "ops.backup_restore_drill"
	// VerbOpsBackupFDBContinuousInit records FoundationDB continuous backup setup.
	VerbOpsBackupFDBContinuousInit Verb = "ops.backup_fdb_continuous_init"
	// VerbOpsDeploy records the full deployment command.
	VerbOpsDeploy Verb = "ops.deploy"
	// VerbOpsDeployBuild records deployment image creation.
	VerbOpsDeployBuild Verb = "ops.deploy_build"
	// VerbOpsDeployPush records deployment image publication.
	VerbOpsDeployPush Verb = "ops.deploy_push"
	// VerbOpsDeployPull records deployment image retrieval.
	VerbOpsDeployPull Verb = "ops.deploy_pull"
	// VerbOpsDeployUp records deployment container rollout.
	VerbOpsDeployUp Verb = "ops.deploy_up"
	// VerbOpsDeployVerify records deployment verification.
	VerbOpsDeployVerify Verb = "ops.deploy_verify"
)

// stateChangeVerbs is the set of application events that must be persisted
// synchronously with the operation they describe. Operator commands declare
// their class in Spec.
var stateChangeVerbs = map[Verb]bool{
	VerbNodeCreate:         true,
	VerbNodeUpdate:         true,
	VerbNodeDelete:         true,
	VerbRelationshipAdd:    true,
	VerbRelationshipRemove: true,
	VerbPropertyDefCreate:  true,
	VerbPropertyDefUpdate:  true,
	VerbPropertyDefDelete:  true,
	VerbNodeTypeCreate:     true,
	VerbNodeTypeUpdate:     true,
	VerbOrgMemberAdd:       true,
	VerbOrgMemberRemove:    true,
	VerbOrgMemberRoleSet:   true,
	VerbUserCreate:         true,
	VerbAuthLoginSucceeded: true,
	VerbAuthLoginFailed:    true,
	VerbAuthTokenCreate:    true,
	VerbAuthTokenRevoke:    true,
	VerbAuditPIIRedacted:   true,
}

func IsRead(v Verb) bool { return !stateChangeVerbs[v] }

// staticToolVerb maps MCP tool names whose names are fixed at build time to
// their verb. Per-NodeType tools (tack_create_<slug>, tack_list_<plural>,
// etc.) are resolved in ToolVerb by prefix.
//
// Every entry resolves to a non-empty verb so the MCP wrapper unconditionally
// records one event per tool call. The TACK-173 FDB-intent path will later
// move state-change emission earlier (inside the FDB transaction) and the
// MCP wrapper will skip when an inner hook already fired; until then the
// MCP boundary is the single canonical site.
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
	"tack_audit_query":         VerbAuditRead,
	"tack_audit_get":           VerbAuditRead,
	"tack_audit_redact_actor":  VerbAuditPIIRedacted,
}

// perTypePrefixVerb maps a tool name prefix (computed from NodeType slug)
// to the right verb. Order matters: longer prefixes first.
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

// ToolVerb returns the verb for a registered MCP tool name, plus a bool
// indicating whether the tool is covered (true) or unknown (false).
//
// A returned empty Verb with covered=true means the tool's audit event is
// emitted by a lower layer; the MCP wrapper must skip it.
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
