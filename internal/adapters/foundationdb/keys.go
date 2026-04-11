//go:build fdb

package foundationdb

import (
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

// FDB key space prefixes — all keys are encoded via the tuple layer.
// Every key is scoped to an org: (prefix, orgID, ...) to ensure tenant isolation.
// See CLAUDE.md for the full key space reference with access patterns.

const (
	// Membership
	keyMembershipByUser   = "membership_by_user"
	keyMembershipByEntity = "membership_by_entity"
	keyMembershipByRole   = "membership_by_role"
	keyInvitation         = "invitation"
	keyInvitationByEmail  = "invitation_by_email"

	// Assignments (replaces SQL issue_assignees, epic_assignees)
	keyAssignmentOnNode = "assignment_on_node"
	keyAssignmentToUser = "assignment_to_user"

	// Labels on nodes (replaces SQL issue_labels, epic_labels)
	keyLabelOnNode     = "label_on_node"
	keyIssuesWithLabel = "issues_with_label"

	// Containment (replaces SQL module_issues, cycle_issues)
	keyIssueInModule          = "issue_in_module"
	keyModulesContainingIssue = "modules_containing_issue"
	keyIssueInCycle           = "issue_in_cycle"
	keyCyclesContainingIssue  = "cycles_containing_issue"

	// Hierarchy (complements SQL parent_id and epic_id columns)
	keyIssueChildren = "issue_children"
	keyEpicChildren  = "epic_children"
	keyIssuesInEpic  = "issues_in_epic"
	// keyIssueEpicReverse maps (orgID, issueID) -> epicID bytes for O(1) reverse lookup.
	keyIssueEpicReverse = "issue_epic_reverse"

	// Custom node instances
	keyNodeInstance          = "node_instance"
	keyNodeInstanceByProject = "node_instance_by_project"
	keyNodeInstanceByState   = "node_instance_by_state"
	keyNodeAssignee          = "node_assignee"
	keyNodeLabel             = "node_label"
	keyNodeParent            = "node_parent"
	keyNodeChildren          = "node_children_of"

	// Relations between nodes
	keyRelationFromNode = "relation_from_node"
	keyRelationToNode   = "relation_to_node"

	// Comments
	keyCommentOnNode     = "comment_on_node"
	keyReplyToComment    = "reply_to_comment"
	keyReactionOnComment = "reaction_on_comment"

	// Activity log
	keyActivityOnNode      = "activity_on_node"
	keyActivityByUser      = "activity_by_user"
	keyActivityOnWorkspace = "activity_on_workspace"

	// Watchers and mentions
	keyWatcherOfNode     = "watcher_of_node"
	keyNodeWatchedByUser = "node_watched_by_user"
	keyMentionInNode     = "mention_in_node"
	keyMentionOfUser     = "mention_of_user"

	// Notifications
	keyNotificationForUser     = "notification_for_user"
	keyUnreadNotificationCount = "unread_notification_count"

	// Counters (atomic)
	keyCountOnNode         = "count_on_node"
	keyCountByState        = "count_by_state"
	keyReactionOnNode      = "reaction_on_node"
	keyReactionCountOnNode = "reaction_count_on_node"

	// Positioning and views
	keySortPositionInView = "sort_position_in_view"
	keyBoardLayoutForUser = "board_layout_for_user"
	keyStarredByUser      = "starred_by_user"
	keySavedViewForUser   = "saved_view_for_user"
	keySavedViewOnEntity  = "saved_view_on_entity"

	// Content
	keyLinkOnNode         = "link_on_node"
	keyAttachmentOnNode   = "attachment_on_node"
	keyDraftForUserOnNode = "draft_for_user_on_node"
	keyDescriptionVersion = "description_version"

	// Work tracking
	keyWorkLogOnNode = "work_log_on_node"
	keyWorkLogByUser = "work_log_by_user"

	// Custom fields
	keyPropertyDefinition  = "property_definition"
	keyPropertyValueOnNode = "property_value_on_node"

	// Custom type definitions
	keyNodeTypeDefinition = "node_type_definition"
	keySequence           = "sequence"

	// Secondary property index — sorted by encoded value for fast filtered listing.
	keyNodeByProperty = "node_by_property"

	// NodeBySequence maps (orgID, projectID, nodeType, sequenceID) → nodeID bytes.
	// Allows O(1) lookup of a node by sequence number within a project.
	// Written atomically alongside the entity on create; cleared on delete.
	keyNodeBySequence = "node_by_sequence"

	// NodeListView materialized read view — mirrors node_instance key structure.
	// (node_list_view, orgID, workspaceID, nodeType, nodeID) → JSON NodeListView
	keyNodeListView = "node_list_view"

	// NodeResolve global resolution record — NOT org-scoped, keyed by nodeID only.
	// Allows Get by nodeID without knowing org/workspace upfront.
	// (node_resolve, nodeID) → JSON NodeResolve
	keyNodeResolve = "node_resolve"

	// Slug-based secondary indexes for structural entity types
	keyOrgBySlug              = "org_by_slug"
	keyWorkspaceBySlug        = "workspace_by_slug"
	keyWorkspaceBySlugGlobal  = "workspace_by_slug_global"
	keyProjectByIdent         = "project_by_identifier"
	keyProjectByWorkspace     = "project_by_workspace"
	keySlugSequence           = "slug_sequence"

	// Automation and rules
	keyAutomationRule          = "automation_rule"
	keyAutomationRuleByTrigger = "automation_rule_by_trigger"
	keyAutomationRunLog        = "automation_run_log"
	keyTransitionRule          = "transition_rule"

	// Settings and roles
	keyUserPreference = "user_preference"
	keyOrgSetting     = "org_setting"
	keyRoleDefinition = "role_definition"
	keyRolePermission = "role_permission"

	// Integrations and ops
	keyWebhook         = "webhook"
	keyWebhookDelivery = "webhook_delivery"
	keySearchSyncState = "search_sync_state"
	keySearchSyncQueue = "search_sync_queue"
	keyAuditLog        = "audit_log"
	keyAuditLogByActor = "audit_log_by_actor"
	keyPresenceOnNode  = "presence_on_node"
)

// orgBySlugKey returns the secondary index key for org lookup by slug.
// (org_by_slug, slug) → orgID bytes
func orgBySlugKey(slug string) tuple.Tuple {
	return tuple.Tuple{keyOrgBySlug, slug}
}

// workspaceBySlugKey returns the secondary index key for workspace lookup by slug.
// (workspace_by_slug, orgID, slug) → wsID bytes
func workspaceBySlugKey(orgID uuid.UUID, slug string) tuple.Tuple {
	return tuple.Tuple{keyWorkspaceBySlug, orgID, slug}
}

// projectByIdentKey returns the secondary index key for project lookup by identifier.
// (project_by_identifier, orgID, wsID, identifier) → projID bytes
func projectByIdentKey(orgID, wsID uuid.UUID, identifier string) tuple.Tuple {
	return tuple.Tuple{keyProjectByIdent, orgID, wsID, identifier}
}

// projectByWorkspaceKey returns the secondary index key for listing projects in a workspace.
// (project_by_workspace, orgID, wsID, projID) → nil
func projectByWorkspaceKey(orgID, wsID, projID uuid.UUID) tuple.Tuple {
	return tuple.Tuple{keyProjectByWorkspace, orgID, wsID, projID}
}

// slugSequenceKey returns the atomic counter key for a slug-owning node.
// (slug_sequence, orgID, slugOwnerNodeID) → int64
// Used instead of keySequence for HasSlug nodes (org, workspace, project, custom containers).
func slugSequenceKey(orgID, slugOwnerNodeID uuid.UUID) tuple.Tuple {
	return tuple.Tuple{keySlugSequence, orgID, slugOwnerNodeID}
}

// _ references keep the canonical key space constants and stub functions from
// triggering the unused linter. These keys are part of the documented FDB schema
// and will be used as features are implemented.
var _ = [...]string{
	keyMembershipByUser, keyMembershipByEntity, keyMembershipByRole,
	keyInvitation, keyInvitationByEmail,
	keyNotificationForUser, keyUnreadNotificationCount,
	keySortPositionInView, keyBoardLayoutForUser, keyStarredByUser,
	keySavedViewForUser, keySavedViewOnEntity,
	keyUserPreference, keyOrgSetting, keyRoleDefinition, keyRolePermission,
	keyWorkspaceBySlug,
	keySlugSequence,
}
var _ = workspaceBySlugKey
var _ = slugSequenceKey
