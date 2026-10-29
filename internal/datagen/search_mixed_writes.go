package datagen

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
)

type searchMixedNode struct {
	id       uuid.UUID
	tenant   int
	ordinal  int
	revision int
	marker   string
	settled  bool
	metadata map[string]string
}

// newSearchMixedMarker returns a marker word: k followed by 12 random hexadecimal characters.
func newSearchMixedMarker() string {
	return opaqueKey(searchMixedMarkerLabel)
}

func (m *searchMixed) issueArguments(tenant *searchMixedTenant, nodeID, project, marker string) ToolArguments {
	properties := newProperties()
	properties.setString("description", "Revision marker "+marker+" run "+m.runKey)
	return ToolArguments{
		WorkspaceReference: tenant.entry(), ProjectReference: project, IssueReference: "",
		Name: "Mixed load " + marker, Properties: properties, NodeID: nodeID, Query: "", NodeType: "",
		Direction: "", SourceID: "", RelationType: "", TargetID: "",
	}
}

func (m *searchMixed) settledNode(newest bool) *searchMixedNode {
	m.nodeMutex.Lock()
	defer m.nodeMutex.Unlock()
	var selected *searchMixedNode
	for _, node := range m.nodes {
		if !node.settled {
			continue
		}
		if selected == nil || newest {
			selected = node
		}
	}
	return selected
}

func (m *searchMixed) settle(node *searchMixedNode, marker string) {
	m.nodeMutex.Lock()
	defer m.nodeMutex.Unlock()
	if node.marker == marker {
		node.settled = true
	}
}

func (m *searchMixed) writeFailed(ctx context.Context, event string, err error) {
	slog.ErrorContext(ctx, event, slog.String("err", err.Error()))
	m.stats.count(&m.stats.writeErrors)
}

// committedAt reads the update time that FoundationDB stores for nodeID after a write.
func (m *searchMixed) committedAt(ctx context.Context, nodeID uuid.UUID) (time.Time, error) {
	view, err := m.stores.Views.Get(ctx, nodeID)
	if err != nil {
		return time.Time{}, loggedError(ctx, "qa datagen search-mixed: the command could not read issue "+nodeID.String(), err)
	}
	if view == nil {
		return time.Time{}, loggedError(ctx, "qa datagen search-mixed: the command could not read issue "+nodeID.String(), errMissing(nil))
	}
	return view.UpdatedAt, nil
}

func (m *searchMixed) createScheduledIssue(callContext context.Context, index int) {
	ordinal := m.created
	m.created++
	tenantIndex := ordinal % len(m.tenants)
	tenant := m.tenants[tenantIndex]
	marker := newSearchMixedMarker()
	arguments, metadata := m.issueArguments(tenant, "", tenant.project, marker), m.metadataValues(tenantIndex)
	for name, value := range metadata {
		arguments.Properties.setString(name, value)
	}
	result, err := m.driver(index).Call(callContext, tenant.token(), "tack_create_issue", arguments)
	if err != nil {
		m.writeFailed(callContext, "qa.datagen.search_mixed_create_failed", err)
		return
	}
	nodeID, err := uuid.Parse(result.RawID())
	if err != nil {
		m.writeFailed(callContext, "qa.datagen.search_mixed_create_id_invalid", err)
		return
	}
	if err := m.registerCreated(callContext, result); err != nil {
		m.writeFailed(callContext, "qa.datagen.search_mixed_create_reference_invalid", err)
		return
	}
	committed, err := m.committedAt(callContext, nodeID)
	if err != nil {
		m.writeFailed(callContext, "qa.datagen.search_mixed_create_time_unread", err)
		return
	}
	node := &searchMixedNode{
		id: nodeID, tenant: tenantIndex, ordinal: ordinal, revision: 1, marker: marker, settled: false, metadata: metadata,
	}
	m.nodeMutex.Lock()
	m.nodes = append(m.nodes, node)
	m.nodeMutex.Unlock()
	m.stats.count(&m.stats.creates)
	m.trackMarker(node, marker, committed)
}

func (m *searchMixed) editIssue(callContext context.Context, index int, node *searchMixedNode) {
	revision := node.revision + 1
	marker := newSearchMixedMarker()
	tenant := m.tenants[node.tenant]
	_, err := m.driver(index).Call(callContext, tenant.token(), "tack_update_issue", m.issueArguments(tenant, node.id.String(), "", marker))
	if err != nil {
		m.writeFailed(callContext, "qa.datagen.search_mixed_edit_failed", err)
		return
	}
	committed, err := m.committedAt(callContext, node.id)
	if err != nil {
		m.writeFailed(callContext, "qa.datagen.search_mixed_edit_time_unread", err)
		return
	}
	m.nodeMutex.Lock()
	node.revision, node.marker, node.settled = revision, marker, false
	m.nodeMutex.Unlock()
	m.stats.count(&m.stats.edits)
	m.trackMarker(node, marker, committed)
}

func (m *searchMixed) deleteIssue(callContext context.Context, index int, node *searchMixedNode) {
	if err := m.deleteNode(callContext, index, node.tenant, node.id); err != nil {
		m.writeFailed(callContext, "qa.datagen.search_mixed_delete_failed", err)
		return
	}
	m.nodeMutex.Lock()
	m.nodes = slices.DeleteFunc(m.nodes, func(candidate *searchMixedNode) bool { return candidate == node })
	marker := node.marker
	m.nodeMutex.Unlock()
	m.stats.count(&m.stats.deletes)
	m.trackDeletion(node, marker)
}

// deleteNode deletes one issue of one tenant through a public call and records its ID.
func (m *searchMixed) deleteNode(ctx context.Context, index, tenant int, nodeID uuid.UUID) error {
	owner := m.tenants[tenant]
	_, err := m.driver(index).Call(ctx, owner.token(), "tack_delete_issue", ToolArguments{
		WorkspaceReference: owner.entry(), ProjectReference: "", IssueReference: "", Name: "", Properties: nil,
		NodeID: nodeID.String(), Query: "", NodeType: "", Direction: "", SourceID: "", RelationType: "", TargetID: "",
	})
	if err != nil {
		return err
	}
	m.nodeMutex.Lock()
	if m.deleted == nil {
		m.deleted = make(map[uuid.UUID]time.Time)
	}
	m.deleted[nodeID] = clock.Now()
	m.nodeMutex.Unlock()
	return nil
}

// deletedBefore reports whether a public delete of nodeID returned before moment.
func (m *searchMixed) deletedBefore(nodeID uuid.UUID, moment time.Time) bool {
	m.nodeMutex.Lock()
	defer m.nodeMutex.Unlock()
	deletedAt, deleted := m.deleted[nodeID]
	return deleted && deletedAt.Before(moment)
}
