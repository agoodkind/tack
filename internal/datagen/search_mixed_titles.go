package datagen

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

const (
	// scopedReferenceSeparator separates a scope's reference from a nested
	// reference.
	scopedReferenceSeparator = "::"
	// sequenceReferenceSeparator separates a project identifier from the
	// sequence number of an issue reference.
	sequenceReferenceSeparator = "-"
	projectIdentifierProperty  = "identifier"
)

// registerEntries records the entry node of every tenant workspace.
func (m *searchMixed) registerEntries(ctx context.Context) error {
	for _, tenant := range m.tenants {
		entryID := node.WorkspaceID(tenant.workspace.OrgID, tenant.workspace.Slug)
		entry, err := m.stores.Views.Get(ctx, entryID)
		if err != nil || entry == nil {
			return loggedError(ctx, "qa datagen search-mixed: the command could not read entry node "+entryID.String(), errMissing(err))
		}
		m.titles.Add(SearchItem{Title: tenant.workspace.Slug, NodeType: entry.NodeType}, entryID)
	}
	return nil
}

// titleFixture returns a fixture that resolves search results with this
// run's SearchTitles registry.
func (m *searchMixed) titleFixture() searchFixture {
	return searchFixture{
		stores: nil, members: nil, nodes: nil, orgID: uuid.Nil, entryID: uuid.Nil,
		typeKey: "", includedKey: "", excludedKey: "", titles: m.titles,
	}
}

// registerCreated records the printed reference and node ID of one write
// confirmation.
func (m *searchMixed) registerCreated(ctx context.Context, result Result) error {
	nodeID, err := uuid.Parse(result.RawID())
	if err != nil {
		return loggedError(ctx, "qa datagen search-mixed: the command could not parse a created node ID", err)
	}
	item := result.SearchItem()
	if item.Title == "" || item.NodeType == "" {
		return loggedError(ctx, "qa datagen search-mixed: the command could not read the printed reference of node "+nodeID.String(), errMissing(nil))
	}
	m.titles.Add(item, nodeID)
	return nil
}

// itemOrg finds the tenant organization for an unregistered result using the
// project identifier at the start of its printed reference. It returns false
// when the tenant projects do not include that identifier.
func (m *searchMixed) itemOrg(ctx context.Context, item SearchItem) (uuid.UUID, bool, error) {
	identifier := referenceScope(item.Title)
	m.projectMutex.Lock()
	defer m.projectMutex.Unlock()
	if orgID, found := m.projectOrgs[identifier]; found {
		return orgID, true, nil
	}
	if err := m.loadProjectOrgs(ctx); err != nil {
		return uuid.Nil, false, err
	}
	orgID, found := m.projectOrgs[identifier]
	return orgID, found, nil
}

// loadProjectOrgs reads every project of every tenant organization and maps
// its identifier to the organization. itemOrg locks projectMutex before the
// read.
func (m *searchMixed) loadProjectOrgs(ctx context.Context) error {
	projectOrgs := make(map[string]uuid.UUID)
	for _, tenant := range m.tenants {
		orgID := tenant.workspace.OrgID
		views, err := m.stores.Views.List(ctx, node.NodeListQuery{OrgID: orgID, NodeType: m.projectType})
		if err != nil {
			return loggedError(ctx, "qa datagen search-mixed: the command could not list the projects of org "+orgID.String(), err)
		}
		for _, view := range views {
			var identifier string
			if err := json.Unmarshal(view.Props[projectIdentifierProperty], &identifier); err != nil {
				return loggedError(ctx, fmt.Sprintf("qa datagen search-mixed: the command could not read the identifier of project %s", view.ID), err)
			}
			projectOrgs[identifier] = orgID
		}
	}
	m.projectOrgs = projectOrgs
	return nil
}
