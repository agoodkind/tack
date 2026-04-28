package audit

import "strings"

// Verb is the stable identifier of what happened. New verbs must be added
// here and to ToolVerb (when MCP-triggerable). The coverage test in
// coverage_test.go fails the build if a registered MCP tool lacks a mapping.
type Verb string

const (
	// State changes -- written synchronously, must not be dropped.
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
	VerbOrgMemberAdd       Verb = "org.member_add"
	VerbOrgMemberRemove    Verb = "org.member_remove"
	VerbOrgMemberRoleSet   Verb = "org.member_role_set"

	// Auth -- every breath, including failures.
	VerbAuthLoginSucceeded Verb = "auth.login_succeeded"
	VerbAuthLoginFailed    Verb = "auth.login_failed"
	VerbAuthTokenCreate    Verb = "auth.token_create"
	VerbAuthTokenRevoke    Verb = "auth.token_revoke"
	VerbAuthTokenUsed      Verb = "auth.token_used"
	VerbAuthTokenRejected  Verb = "auth.token_rejected"

	// Reads -- batched via WAL for hot-path tolerance.
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
)

// stateChangeVerbs is the set of verbs that must be persisted synchronously
// with the operation they describe. Anything not in this set is read-class.
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
	VerbAuthLoginSucceeded: true,
	VerbAuthLoginFailed:    true,
	VerbAuthTokenCreate:    true,
	VerbAuthTokenRevoke:    true,
	VerbAuditPIIRedacted:   true,
}

func IsStateChange(v Verb) bool { return stateChangeVerbs[v] }
func IsRead(v Verb) bool        { return !stateChangeVerbs[v] }

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
