package foundationdb

import (
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

// FDB key space prefixes: all keys are encoded via the tuple layer.
// Every key is scoped to an org: (prefix, orgID, ...) to ensure tenant isolation.
// See CLAUDE.md for the full key space reference with access patterns.

const (
	// Membership
	keyMembershipByUser   = "membership_by_user"
	keyMembershipByEntity = "membership_by_entity"
	keyMembershipByRole   = "membership_by_role"
	keyInvitation         = "invitation"
	keyInvitationByEmail  = "invitation_by_email"

	// Assignments
	keyAssignmentOnNode = "assignment_on_node"
	keyAssignmentToUser = "assignment_to_user"

	// Labels on nodes
	keyLabelOnNode     = "label_on_node"
	keyNodesWithLabel = "issues_with_label"

	// Containment: generic parent-child links between any node types.
	// (containment, orgID, containerID, childID) → ContainmentValue JSON
	keyContainment = "containment"
	// (containment_reverse, orgID, childID, containerID) → nil
	keyContainmentReverse = "containment_reverse"

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

	// Secondary property index: sorted by encoded value for fast filtered listing.
	keyNodeByProperty = "node_by_property"

	// NodeBySequence maps (orgID, projectID, nodeType, sequenceID) → nodeID bytes.
	// Allows O(1) lookup of a node by sequence number within a project.
	// Written atomically alongside the entity on create; cleared on delete.
	keyNodeBySequence = "node_by_sequence"

	// NodeListView materialized read view: mirrors node_instance key structure.
	// (node_list_view, orgID, workspaceID, nodeType, nodeID) → JSON NodeListView
	keyNodeListView = "node_list_view"

	// NodeResolve global resolution record: NOT org-scoped, keyed by nodeID only.
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

// slugSequenceKey returns the packed atomic counter key for a slug-owning node.
// (slug_sequence, orgID, slugOwnerNodeID) → int64
func slugSequenceKey(orgID, slugOwnerNodeID uuid.UUID) []byte {
	return tuple.Tuple{keySlugSequence, orgID.String(), slugOwnerNodeID.String()}.Pack()
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
	keyOrgBySlug, keyWorkspaceBySlug, keyWorkspaceBySlugGlobal,
	keyProjectByIdent, keyProjectByWorkspace, keySlugSequence,
}
var _ = slugSequenceKey
