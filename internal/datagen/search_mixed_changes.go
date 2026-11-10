package datagen

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

const (
	// searchMixedMetadataLabel is the prefix of each excluded property definition name.
	searchMixedMetadataLabel = "pm"
	// searchMixedDestinationLabel is the prefix of each destination project identifier.
	searchMixedDestinationLabel = "d"
)

// searchMixedChangeKind distinguishes a metadata change from an issue move.
// Either kind replaces one scheduled create, edit, or delete.
type searchMixedChangeKind int

const (
	searchMixedMetadataChange searchMixedChangeKind = iota + 1
	searchMixedMoveChange
)

// searchMixedChanges stores the state of metadata changes and moves for one run.
type searchMixedChanges struct {
	// definitions stores property definitions in change order for the first tenant's organization.
	// Setup excludes their values from search.
	definitions []*node.PropertyDef
	// changed counts the definitions that metadata changes have used. Only the write loop goroutine
	// reads and writes it.
	changed int
	// destinations stores one destination project per tenant in tenant order.
	destinations []searchMixedDestination
	// nodes is the service.NodeService that moves issues.
	nodes *service.NodeService
}

// searchMixedDestination identifies a project in a second workspace of one tenant's organization.
type searchMixedDestination struct {
	entry     string
	projectID uuid.UUID
}

// searchMixedCheck tracks a committed metadata change or move that the prober has not verified.
// marker is the property value for a metadata change or the issue marker for a move.
type searchMixedCheck struct {
	node   *searchMixedNode
	marker string
}

// prepareChanges returns immediately when both requested counts are 0. Otherwise, it enables
// search work on the FoundationDB stores and stores one text property definition per metadata
// change in the first tenant's organization with Search.Include set to false. For requested
// moves, it builds the node service and creates one destination project per tenant.
func (m *searchMixed) prepareChanges(ctx context.Context, identities Identities) error {
	if m.options.MetadataWrites == 0 && m.options.Moves == 0 {
		return nil
	}
	m.stores.EnableSearchWork()
	orgID := m.tenants[0].workspace.OrgID
	for range m.options.MetadataWrites {
		definition := searchDefinition(orgID, opaqueKey(searchMixedMetadataLabel), false, 0)
		if err := m.stores.PropertyDefs.Set(ctx, definition); err != nil {
			return loggedError(ctx, "qa datagen search-mixed: the command could not store property definition "+definition.Name, err)
		}
		m.changes.definitions = append(m.changes.definitions, definition)
	}
	if m.options.Moves == 0 {
		return nil
	}
	stores := m.stores
	nodes, err := service.NewNodeService(stores.Nodes, stores.Views, stores.NodeTypes, stores.PropertyDefs,
		stores.Relationships, stores.NodeDeleter, m.members, stores.Relationships)
	if err != nil {
		return loggedError(ctx, "qa datagen search-mixed: the command could not build the node service for moves", err)
	}
	m.changes.nodes = nodes
	for index, tenant := range m.tenants {
		destination, err := m.createDestination(ctx, index, identities, tenant)
		if err != nil {
			return err
		}
		m.changes.destinations = append(m.changes.destinations, destination)
	}
	return nil
}

// createDestination finds another workspace in the tenant's organization and records its entry
// node's title. It creates a project through a public tack_create_project call and records the
// project's title. It returns the workspace slug and project ID, or an error if the organization
// has one workspace.
func (m *searchMixed) createDestination(
	ctx context.Context, index int, identities Identities, tenant *searchMixedTenant,
) (searchMixedDestination, error) {
	none := searchMixedDestination{entry: "", projectID: uuid.Nil}
	slug := ""
	for _, workspace := range identities.Workspaces {
		if workspace.OrgID == tenant.workspace.OrgID && workspace.Slug != tenant.entry() {
			slug = workspace.Slug
			break
		}
	}
	if slug == "" {
		return none, fmt.Errorf("qa datagen search-mixed: organization %s has one workspace, and moves require a second workspace", tenant.workspace.OrgID)
	}
	if err := m.registerEntry(ctx, tenant.workspace.OrgID, slug); err != nil {
		return none, err
	}
	properties := newProperties()
	properties.setString("identifier", strings.ToUpper(opaqueKey(searchMixedDestinationLabel)))
	result, err := m.driver(index).Call(ctx, tenant.token(), "tack_create_project", ToolArguments{
		WorkspaceReference: slug, ProjectReference: "", IssueReference: "",
		Name: "Search mixed moves " + m.runKey, Properties: properties, NodeID: "", Query: "", NodeType: "",
		Direction: "", SourceID: "", RelationType: "", TargetID: "",
	})
	if err != nil {
		return none, loggedError(ctx, "qa datagen search-mixed: the command could not create a destination project in "+slug, err)
	}
	if err := m.registerCreated(ctx, result); err != nil {
		return none, err
	}
	projectID, err := uuid.Parse(result.RawID())
	if err != nil {
		return none, loggedError(ctx, "qa datagen search-mixed: the command could not parse the destination project ID in "+slug, err)
	}
	return searchMixedDestination{entry: slug, projectID: projectID}, nil
}

// searchMixedChangeSlots assigns metadata changes and moves to write indexes.
// For total = metadata+moves, position p uses write index (2p+1)*planned/(2*total).
// While both kinds remain, even positions use metadata changes and odd positions use moves.
// After one kind runs out, every remaining position uses the other kind.
func searchMixedChangeSlots(planned, metadata, moves int) map[int]searchMixedChangeKind {
	total := metadata + moves
	slots := make(map[int]searchMixedChangeKind, total)
	for position := range total {
		index := (2*position + 1) * planned / (2 * total)
		if metadata > 0 && (position%2 == 0 || moves == 0) {
			slots[index] = searchMixedMetadataChange
			metadata--
			continue
		}
		slots[index] = searchMixedMoveChange
		moves--
	}
	return slots
}

// writeChange runs one metadata change or move. It returns false if an issue is not available.
func (m *searchMixed) writeChange(ctx context.Context, kind searchMixedChangeKind) bool {
	if kind == searchMixedMetadataChange {
		return m.changeMetadata(ctx)
	}
	return m.moveIssue(ctx)
}

// pinNode locks the node mutex and selects the first node that match accepts and the prober
// has already found for its marker. If it cannot find one, it selects the first matching node.
// It removes that node from selection for later edits, deletes, metadata changes, and moves.
// It returns the node and its current marker, or nil and "" if it cannot find a matching node.
func (m *searchMixed) pinNode(match func(*searchMixedNode) bool) (*searchMixedNode, string) {
	m.nodeMutex.Lock()
	defer m.nodeMutex.Unlock()
	position := -1
	for index, candidate := range m.nodes {
		if !match(candidate) {
			continue
		}
		if candidate.settled {
			position = index
			break
		}
		if position < 0 {
			position = index
		}
	}
	if position < 0 {
		return nil, ""
	}
	pinned := m.nodes[position]
	m.nodes = slices.Delete(m.nodes, position, position+1)
	return pinned, pinned.marker
}
