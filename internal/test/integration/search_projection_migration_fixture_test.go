package integration_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	migrationEngineMemory = int64(8 << 30)
	migrationIdleWait     = 2 * time.Minute
	migrationExcluded     = "EXCLUDED-MIGRATION-VALUE"
)

// migrationEnv provides one organization with an included property and an
// excluded property of unfamiliar types. It also provides a serving
// OpenSearch index and the production search worker.
type migrationEnv struct {
	Stores      *fdbadapter.Stores
	Adapter     *search.Adapter
	Client      *opensearchapi.Client
	Index       string
	OrgID       uuid.UUID
	TypeKey     string
	IncludedKey string
	ExcludedKey string
	PageBytes   int
	Worker      *service.SearchWorker
}

// migrationPage stores one page document that the test reads from OpenSearch.
type migrationPage struct {
	NodeRevision      string  `json:"node_revision"`
	ProjectionVersion string  `json:"projection_version"`
	PageOrdinal       uint64  `json:"page_ordinal"`
	PageText          *string `json:"page_text"`
}

func uniqueKey(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")
}

// newMigrationEnv stores the metadata of a new organization and provisions
// the model and one serving index.
func newMigrationEnv(t *testing.T, pageBytes int) *migrationEnv {
	t.Helper()
	stores := integration.SetupTestEnv(t).Stores
	stores.EnableSearchWork()
	fixture := testenv.OpenSearchWithMemory(t, migrationEngineMemory)
	configuration := search.Config{
		Endpoint: fixture.Endpoint, CA: fixture.CA, Username: fixture.Username,
		Password: fixture.Password, RequestTimeout: 2 * time.Minute, MaxRetries: 1,
	}
	adapter, err := search.New(integration.WithTestOperation(t, t.Context()), configuration)
	if err != nil {
		t.Fatalf("create search adapter: %v", err)
	}
	t.Cleanup(func() {
		if err := adapter.Close(context.WithoutCancel(integration.WithTestOperation(t, t.Context()))); err != nil {
			t.Errorf("close search adapter: %v", err)
		}
	})
	raw, err := opensearch.NewClient(configuration.ClientConfig(nil))
	if err != nil {
		t.Fatalf("create OpenSearch client: %v", err)
	}
	t.Cleanup(func() {
		if err := raw.Close(); err != nil {
			t.Errorf("close OpenSearch client: %v", err)
		}
	})
	client := opensearchapi.NewFromClient(raw)
	model, err := adapter.Provision(integration.WithTestOperation(t, t.Context()))
	if err != nil {
		t.Fatalf("provision search model: %v", err)
	}
	index := "node-pages-" + uuid.Must(uuid.NewV7()).String()
	spec := search.IndexSpec{Model: model, MappingVersion: search.MappingVersion, Primaries: 1, RoutingShards: 8, Replicas: 0}
	if err := adapter.EnsureIndex(integration.WithTestOperation(t, t.Context()), index, spec); err != nil {
		t.Fatalf("create index %s: %v", index, err)
	}
	t.Cleanup(func() {
		if _, err := client.Indices.Delete(context.WithoutCancel(integration.WithTestOperation(t, t.Context())), opensearchapi.IndicesDeleteReq{Indices: []string{index}}); err != nil {
			t.Errorf("delete index %s: %v", index, err)
		}
	})
	if err := stores.InitializeSearchIndex(integration.WithTestOperation(t, t.Context()), index); err != nil {
		t.Fatalf("record serving index: %v", err)
	}
	env := &migrationEnv{
		Stores: stores, Adapter: adapter, Client: client, Index: index, OrgID: uuid.Must(uuid.NewV7()),
		TypeKey: uniqueKey("n"), IncludedKey: uniqueKey("p"), ExcludedKey: uniqueKey("p"), PageBytes: pageBytes, Worker: nil,
	}
	definitionIDs := []uuid.UUID{env.putDefinition(t, env.IncludedKey, true), env.putDefinition(t, env.ExcludedKey, false)}
	kind := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, Name: env.TypeKey, TypeKey: env.TypeKey, Slug: env.TypeKey,
		PluralSlug: env.TypeKey + "s", PropertyDefIDs: definitionIDs, Features: node.Features{node.FeatureIsEntryPoint},
	}
	if err := stores.NodeTypes.Set(integration.WithTestOperation(t, t.Context()), kind); err != nil {
		t.Fatalf("store node type: %v", err)
	}
	env.Worker = env.newWorker(t, stores)
	return env
}

// newWorker creates the production worker with stores and the adapter.
func (env *migrationEnv) newWorker(t *testing.T, stores *fdbadapter.Stores) *service.SearchWorker {
	t.Helper()
	policies := stores.SearchPolicySet()
	rollouts := stores.SearchRollouts(clock.Wall{}, policies)
	settings := config.SearchWorkerSettings{
		PageBytes: env.PageBytes, MaxPages: 32, MaxBytes: 5 << 20, SliceBudget: 2 * time.Second,
		OperationTimeout: 10 * time.Second, Lease: 30 * time.Second, IdleInterval: 50 * time.Millisecond,
		Concurrency: 1, ClassWeights: map[string]int{"live": 1, "access": 1, "cleanup": 1, "rescan": 1, "rollout": 1, "rebuild": 1, "copy": 1},
		ReplacementPauseLimit: config.SearchReplacementPauseLimit,
	}
	worker, err := service.NewSearchWorker(service.SearchWorkerPorts{
		Store: stores.SearchWork(clock.Wall{}), Reader: stores.SearchContent(policies),
		Access: stores.SearchAccess(policies), Writer: env.Adapter, Rollouts: rollouts, Sessions: rollouts,
		Documents: env.Adapter, Rebuilds: stores.SearchRebuilds(clock.Wall{}), Replacer: env.Adapter,
	}, clock.Wall{}, settings, 0)
	if err != nil {
		t.Fatalf("create search worker: %v", err)
	}
	return worker
}

// putDefinition stores one definition with scalar text rendering.
// It sets search inclusion to include and returns the definition's ID.
func (env *migrationEnv) putDefinition(t *testing.T, name string, include bool) uuid.UUID {
	t.Helper()
	definition := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, Name: name, Type: node.PropertyType(uniqueKey("t")),
		Search: &node.SearchProjection{Include: include, Order: 1, Rule: node.TextRule{Mode: node.TextRuleScalar}},
	}
	if err := env.Stores.PropertyDefs.Set(integration.WithTestOperation(t, t.Context()), definition); err != nil {
		t.Fatalf("store property definition %s: %v", name, err)
	}
	return definition.ID
}

// putNode stores one node and sets its included property to included.
func (env *migrationEnv) putNode(t *testing.T, nodeID uuid.UUID, included string) {
	t.Helper()
	env.putNodeProps(t, nodeID, map[string]json.RawMessage{env.IncludedKey: encodeText(t, included), env.ExcludedKey: encodeText(t, migrationExcluded)})
}

func (env *migrationEnv) putNodeProps(t *testing.T, nodeID uuid.UUID, props map[string]json.RawMessage) {
	t.Helper()
	now := clock.Now().UTC()
	value := &node.Node{ID: nodeID, OrgID: env.OrgID, NodeType: env.TypeKey, Name: "Migration node", Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: nodeID, OrgID: env.OrgID, NodeType: env.TypeKey, Name: "Migration node", Props: props, CreatedAt: now, UpdatedAt: now}
	if err := env.Stores.Nodes.Set(integration.WithTestOperation(t, t.Context()), value, view); err != nil {
		t.Fatalf("store node %s: %v", nodeID, err)
	}
}

// putNodes stores count nodes with included and returns their IDs.
func (env *migrationEnv) putNodes(t *testing.T, count int, included string) []uuid.UUID {
	t.Helper()
	nodes := make([]uuid.UUID, 0, count)
	for range count {
		nodeID := uuid.Must(uuid.NewV7())
		env.putNode(t, nodeID, included)
		nodes = append(nodes, nodeID)
	}
	return nodes
}

func encodeText(t *testing.T, text string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("encode %q: %v", text, err)
	}
	return encoded
}
