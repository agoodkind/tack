package datagen

import (
	"context"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// registerEntries records the entry node of every tenant workspace.
func (m *searchMixed) registerEntries(ctx context.Context) error {
	for _, tenant := range m.tenants {
		if err := m.registerEntry(ctx, tenant.workspace.OrgID, tenant.workspace.Slug); err != nil {
			return err
		}
	}
	return nil
}

// registerEntry reads the workspace entry node for slug in orgID and records its title.
func (m *searchMixed) registerEntry(ctx context.Context, orgID uuid.UUID, slug string) error {
	entryID := node.WorkspaceID(orgID, slug)
	entry, err := m.stores.Views.Get(ctx, entryID)
	if err != nil || entry == nil {
		return loggedError(ctx, "qa datagen search-mixed: the command could not read entry node "+entryID.String(), errMissing(err))
	}
	m.titles.Add(SearchItem{Title: slug, NodeType: entry.NodeType}, entryID)
	return nil
}

// titleFixture returns a fixture with the shared title registry and the
// selected tenant's organization and stores for result resolution.
func (m *searchMixed) titleFixture(tenant int) searchFixture {
	return searchFixture{
		stores: m.stores, members: nil, nodes: nil, orgID: m.tenants[tenant].workspace.OrgID, entryID: uuid.Nil,
		typeKey: "", includedKey: "", excludedKey: "", titles: m.titles,
	}
}

// identities resolves results with driver using the tenant's token, entry,
// and organization.
func (m *searchMixed) identities(tenant int, driver *Driver) SearchIdentities {
	owner := m.tenants[tenant]
	return m.titleFixture(tenant).identities(driver, owner.token(), owner.entry())
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
