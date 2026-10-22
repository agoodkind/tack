package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
)

type searchMixedNode struct {
	id       uuid.UUID
	ordinal  int
	revision int
	marker   string
	settled  bool
}

func (m *searchMixed) marker(ordinal, revision int) string {
	return fmt.Sprintf("%sn%dr%d", m.runKey, ordinal, revision)
}

func (m *searchMixed) issueArguments(nodeID, project, marker string) ToolArguments {
	properties := newProperties()
	properties.setString("description", "Revision marker "+marker)
	return ToolArguments{
		WorkspaceReference: m.load.entry, ProjectReference: project, IssueReference: "",
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

func (m *searchMixed) createIssue(callContext, probeContext context.Context) {
	ordinal := m.created
	m.created++
	marker := m.marker(ordinal, 1)
	result, err := m.load.driver.Call(callContext, m.load.token, "tack_create_issue", m.issueArguments("", m.project, marker))
	committed := clock.Now()
	if err != nil {
		m.writeFailed(callContext, "qa.datagen.search_mixed_create_failed", err)
		return
	}
	nodeID, err := uuid.Parse(result.RawID())
	if err != nil {
		m.writeFailed(callContext, "qa.datagen.search_mixed_create_id_invalid", err)
		return
	}
	node := &searchMixedNode{id: nodeID, ordinal: ordinal, revision: 1, marker: marker, settled: false}
	m.nodeMutex.Lock()
	m.nodes = append(m.nodes, node)
	m.nodeMutex.Unlock()
	m.stats.count(&m.stats.creates)
	m.probe(probeContext, func(ctx context.Context) { m.awaitMarker(ctx, node, marker, committed) })
}

func (m *searchMixed) editIssue(callContext, probeContext context.Context, node *searchMixedNode) {
	revision := node.revision + 1
	marker := m.marker(node.ordinal, revision)
	_, err := m.load.driver.Call(callContext, m.load.token, "tack_update_issue", m.issueArguments(node.id.String(), "", marker))
	committed := clock.Now()
	if err != nil {
		m.writeFailed(callContext, "qa.datagen.search_mixed_edit_failed", err)
		return
	}
	m.nodeMutex.Lock()
	node.revision, node.marker, node.settled = revision, marker, false
	m.nodeMutex.Unlock()
	m.stats.count(&m.stats.edits)
	m.probe(probeContext, func(ctx context.Context) { m.awaitMarker(ctx, node, marker, committed) })
}

func (m *searchMixed) deleteIssue(callContext, probeContext context.Context, node *searchMixedNode) {
	_, err := m.load.driver.Call(callContext, m.load.token, "tack_delete_issue", ToolArguments{
		WorkspaceReference: m.load.entry, ProjectReference: "", IssueReference: "", Name: "", Properties: nil,
		NodeID: node.id.String(), Query: "", NodeType: "", Direction: "", SourceID: "", RelationType: "", TargetID: "",
	})
	if err != nil {
		m.writeFailed(callContext, "qa.datagen.search_mixed_delete_failed", err)
		return
	}
	m.nodeMutex.Lock()
	m.nodes = slices.DeleteFunc(m.nodes, func(candidate *searchMixedNode) bool { return candidate == node })
	marker := node.marker
	m.nodeMutex.Unlock()
	m.stats.count(&m.stats.deletes)
	m.probe(probeContext, func(ctx context.Context) { m.awaitDeletion(ctx, node.id, marker) })
}
