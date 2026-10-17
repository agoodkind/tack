package integration_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/service"
)

// parentEdgeKind is one opaque node type with one included and one excluded
// property.
type parentEdgeKind struct {
	orgID              uuid.UUID
	typeKey            string
	included, excluded string
}

// parentEdgeFixture is one production graph over real FoundationDB,
// OpenSearch, and the ledger, with two entry points of one organization and
// an opaque leaf type. The leaf type lists only the entry-point types as
// parents. No metadata declares a leaf under a leaf. Writes go through the
// production node service, and the test runs every search worker slice.
type parentEdgeFixture struct {
	cfg                     *config.Config
	stores                  *fdbadapter.Stores
	adapter                 *search.Adapter
	worker                  *service.SearchWorker
	driver                  *datagen.Driver
	nodes                   *service.NodeService
	first, second, foreign  datagen.WorkspaceIdentity
	firstEntry, secondEntry uuid.UUID
	entryTypes              []string
	kind                    parentEdgeKind
}

func newParentEdgeFixture(t *testing.T) parentEdgeFixture {
	t.Helper()
	cfg := newCohortEngines(t)
	graph, err := appruntime.BuildGraph(t.Context(), cfg)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	t.Cleanup(graph.Close)
	scale, err := datagen.ParseScale("medium")
	if err != nil {
		t.Fatalf("parse scale: %v", err)
	}
	seed := clock.Now().UnixNano()
	identities, err := datagen.BootstrapIdentities(t.Context(), cfg, seed, scale)
	if err != nil {
		t.Fatalf("bootstrap identities: %v", err)
	}
	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, cohortTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	stores.EnableSearchWork()
	fixture := parentEdgeFixture{
		cfg: cfg, stores: stores, driver: datagen.NewDriver(graph, false, seed),
		nodes: service.NewNodeService(stores.Nodes, stores.Views, stores.NodeTypes, stores.PropertyDefs, stores.Relationships, stores.NodeDeleter),
	}
	fixture.worker, fixture.adapter = newParentEdgeWorker(t, cfg, stores)
	fixture.first, fixture.second, fixture.foreign = parentEdgeWorkspaces(t, identities.Workspaces)
	fixture.entryTypes = parentEdgeEntryTypes(t, stores, fixture.first.OrgID)
	fixture.firstEntry, fixture.secondEntry = fixture.entry(t, fixture.first), fixture.entry(t, fixture.second)
	fixture.kind = fixture.putKind(t, fixture.first.OrgID, fixture.entryTypes)
	return fixture
}

// newParentEdgeWorker builds one production search worker over stores and
// the configured OpenSearch engine. It returns the worker and its adapter.
func newParentEdgeWorker(t *testing.T, cfg *config.Config, stores *fdbadapter.Stores) (*service.SearchWorker, *search.Adapter) {
	t.Helper()
	settings, err := config.LoadSearchWorkerSettings(t.Context())
	if err != nil {
		t.Fatalf("load search worker settings: %v", err)
	}
	certificate, err := config.LoadSearchCA(t.Context(), cfg.SearchCA)
	if err != nil {
		t.Fatalf("load search CA: %v", err)
	}
	adapter, err := search.New(t.Context(), search.Config{
		Endpoint: cfg.SearchEndpoint, CA: certificate, Username: cfg.SearchUsername,
		Password: cfg.SearchPassword, RequestTimeout: cfg.SearchRequestTimeout, MaxRetries: cfg.SearchMaxRetries,
	})
	if err != nil {
		t.Fatalf("open search adapter: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close(context.WithoutCancel(t.Context())) })
	policies := stores.SearchPolicySet()
	rollouts := stores.SearchRollouts(clock.Wall{}, policies)
	worker, err := service.NewSearchWorker(service.SearchWorkerPorts{
		Store: stores.SearchWork(clock.Wall{}), Reader: stores.SearchContent(policies),
		Access: stores.SearchAccess(policies), Writer: adapter,
		Rollouts: rollouts, Sessions: rollouts, Documents: adapter,
		Rebuilds: stores.SearchRebuilds(clock.Wall{}), Replacer: adapter,
	}, clock.Wall{}, settings, 0)
	if err != nil {
		t.Fatalf("create search worker: %v", err)
	}
	return worker, adapter
}

// parentEdgeWorkspaces returns two workspaces of one organization and one
// workspace of another organization.
func parentEdgeWorkspaces(t *testing.T, workspaces []datagen.WorkspaceIdentity) (first, second, foreign datagen.WorkspaceIdentity) {
	t.Helper()
	first = workspaces[0]
	foundSecond, foundForeign := false, false
	for _, workspace := range workspaces[1:] {
		if workspace.OrgID == first.OrgID && !foundSecond {
			second, foundSecond = workspace, true
		}
		if workspace.OrgID != first.OrgID && !foundForeign {
			foreign, foundForeign = workspace, true
		}
	}
	if !foundSecond || !foundForeign {
		t.Fatalf("the bootstrapped workspaces lack a second workspace or a second organization")
	}
	return first, second, foreign
}

// parentEdgeEntryTypes returns the type keys of the entry-point node types
// of orgID.
func parentEdgeEntryTypes(t *testing.T, stores *fdbadapter.Stores, orgID uuid.UUID) []string {
	t.Helper()
	types, err := stores.NodeTypes.List(t.Context(), orgID)
	if err != nil {
		t.Fatalf("list node types of organization %s: %v", orgID, err)
	}
	keys := []string{}
	for _, kind := range types {
		if kind.Features.Has(node.FeatureIsEntryPoint) {
			keys = append(keys, kind.TypeKey)
		}
	}
	return keys
}

func (f parentEdgeFixture) entry(t *testing.T, workspace datagen.WorkspaceIdentity) uuid.UUID {
	t.Helper()
	for _, typeKey := range parentEdgeEntryTypes(t, f.stores, workspace.OrgID) {
		views, err := f.stores.Views.List(t.Context(), node.NodeListQuery{OrgID: workspace.OrgID, NodeType: typeKey})
		if err != nil {
			t.Fatalf("list entry nodes of type %s: %v", typeKey, err)
		}
		for _, view := range views {
			if view.Name == workspace.Name {
				return view.ID
			}
		}
	}
	t.Fatalf("workspace %s has no entry node", workspace.Slug)
	return uuid.Nil
}

// putKind stores one opaque node type of orgID with canLiveUnder as its
// parent types, and its two property definitions.
func (f parentEdgeFixture) putKind(t *testing.T, orgID uuid.UUID, canLiveUnder []string) parentEdgeKind {
	t.Helper()
	kind := parentEdgeKind{orgID: orgID, typeKey: parentEdgeKey("n"), included: parentEdgeKey("p"), excluded: parentEdgeKey("x")}
	definitionIDs := []uuid.UUID{}
	for order, name := range []string{kind.included, kind.excluded} {
		definition := &node.PropertyDef{
			ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: name, Type: node.PropertyType(parentEdgeKey("t")),
			Search: &node.SearchProjection{Include: order == 0, Order: order, Rule: node.TextRule{Mode: node.TextRuleScalar}},
		}
		if err := f.stores.PropertyDefs.Set(t.Context(), definition); err != nil {
			t.Fatalf("store property definition %s: %v", name, err)
		}
		definitionIDs = append(definitionIDs, definition.ID)
	}
	nodeType := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: kind.typeKey, TypeKey: kind.typeKey, Slug: kind.typeKey,
		PluralSlug: kind.typeKey + "s", PropertyDefIDs: definitionIDs, CanLiveUnder: canLiveUnder,
	}
	if err := f.stores.NodeTypes.Set(t.Context(), nodeType); err != nil {
		t.Fatalf("store node type %s: %v", kind.typeKey, err)
	}
	return kind
}

func parentEdgeKey(label string) string {
	return label + strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")[20:]
}
