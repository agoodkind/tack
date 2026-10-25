package integration

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/ops"
)

// TestSearchProjectionReadiness verifies that each audited search command
// rejects an invalid stored search declaration, and then an unknown property
// type without a search declaration, before requesting OpenSearch. It then
// assigns the definition a known type without a declaration and verifies that
// provisioning succeeds with the default projection.
func TestSearchProjectionReadiness(t *testing.T) {
	env := SetupTestEnv(t)
	control := newSearchControl(t)
	definitions, err := env.Stores.PropertyDefs.List(env.Ctx, env.OrgID)
	if err != nil || len(definitions) == 0 || definitions[0].Search == nil {
		t.Fatalf("list declared definitions: count = %d, error = %v", len(definitions), err)
	}
	invalid := *definitions[0]
	invalid.Search = &node.SearchProjection{Include: true, Order: 0, Rule: node.TextRule{Mode: node.TextRuleLabels}}
	if err := env.Stores.PropertyDefs.Set(env.Ctx, &invalid); err != nil {
		t.Fatalf("store a definition with an invalid search declaration: %v", err)
	}
	requireSearchCommandsRefuse(t, env, control, "labels search rule requires a label map")
	undecidable := *definitions[0]
	undecidable.Search = nil
	undecidable.Type = node.PropertyType(opaqueSearchKey("t"))
	if err := env.Stores.PropertyDefs.Set(env.Ctx, &undecidable); err != nil {
		t.Fatalf("store a definition without a default projection: %v", err)
	}
	requireSearchCommandsRefuse(t, env, control, "1 definitions without a search declaration lack a default projection")
	defaulted := undecidable
	defaulted.Type = node.PropertyTypeText
	if err := env.Stores.PropertyDefs.Set(env.Ctx, &defaulted); err != nil {
		t.Fatalf("store a definition with a default projection: %v", err)
	}
	if err := control.run("ops", "search", "provision"); err != nil {
		t.Fatalf("provision with a defaulted definition: %v", err)
	}
}

// requireSearchCommandsRefuse requires provision, reindex, and verify to fail
// with an error that contains want. It also requires that none of them
// created the control index or began an index replacement.
func requireSearchCommandsRefuse(t *testing.T, env *TestEnv, control searchControl, want string) {
	t.Helper()
	for _, command := range []string{"provision", "reindex", "verify"} {
		err := control.run("ops", "search", command)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s with an unready projection: error = %v, want %q", command, err, want)
		}
		if searchIndexExists(t, control, searchControlIndex) {
			t.Fatalf("%s created %s while a projection was unready", command, searchControlIndex)
		}
	}
	rebuild, running, err := env.Stores.SearchRebuilds(clock.Wall{}).CurrentRebuild(env.Ctx)
	if err != nil || running {
		t.Fatalf("index replacement after refused commands = %+v, running %t, error %v", rebuild, running, err)
	}
}

// warningFailureSlices limits the number of slices within which a read must
// fail.
const warningFailureSlices = 10

// TestSearchProjectionWarningLifecycle verifies that registering the first
// page stages an undeclared property warning without publishing it. The
// production worker publishes the warning after indexing completes. A later
// revision's failed source read preserves it. Completed node deletion
// removes it.
func TestSearchProjectionWarningLifecycle(t *testing.T) {
	stores := newSearchStore(t)
	adapter, _, _, _ := newSearchIndex(t, stores)
	source := clock.Wall{}
	settings := searchWorkerSettings(readerPageBytes)
	fixture := putSearchText(t, stores, "lifecycle text", readerExcludedValue)
	undeclared := opaqueSearchKey("p")
	writeUndeclaredSearchNode(t, stores, fixture, undeclared)

	workStore := stores.SearchWork(source)
	work, err := workStore.Claim(WithTestOperation(t, t.Context()), searchdomain.WorkClassLive, "staging-owner", settings.Lease)
	if err != nil || work.NodeID != fixture.NodeID {
		t.Fatalf("claim live work = %+v, error = %v, want node %s", work, err, fixture.NodeID)
	}
	page, err := stores.SearchContent(stores.SearchPolicySet()).Content(WithTestOperation(t, t.Context()), searchdomain.ContentRequest{
		NodeID: fixture.NodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil, MaxBytes: readerPageBytes, SearchGeneration: work.Generation,
	})
	if err != nil {
		t.Fatalf("read the first page: %v", err)
	}
	if _, err := workStore.Register(WithTestOperation(t, t.Context()), work, page); err != nil {
		t.Fatalf("register the first page: %v", err)
	}
	requireNodeWarnings(t, stores, fixture.NodeID, nil)
	if err := workStore.Yield(WithTestOperation(t, t.Context()), work); err != nil {
		t.Fatalf("yield the staging claim: %v", err)
	}
	runSearchWorkerUntilIdle(t, newSearchWorker(t, stores, adapter, source, settings))
	requireNodeWarnings(t, stores, fixture.NodeID, []string{undeclared})

	writeUndeclaredSearchNode(t, stores, fixture, undeclared)
	clearKeyFamily(t, "node_type_by_key")
	failing := newSearchWorker(t, stores, adapter, source, settings)
	failed := false
	for range warningFailureSlices {
		if _, err := failing.RunSlice(WithTestOperation(t, t.Context())); err != nil {
			failed = true
			break
		}
	}
	if !failed {
		t.Fatalf("%d slices without the node type index succeeded, want a failed source read", warningFailureSlices)
	}
	requireNodeWarnings(t, stores, fixture.NodeID, []string{undeclared})

	if _, err := ops.RunMetadataNameBackfill(WithTestOperation(t, t.Context()), stores.NodeTypes, stores.PropertyDefs, false); err != nil {
		t.Fatalf("restore the node type index: %v", err)
	}
	if err := stores.Nodes.Delete(WithTestOperation(t, t.Context()), fixture.OrgID, fixture.NodeID); err != nil {
		t.Fatalf("delete node %s: %v", fixture.NodeID, err)
	}
	runSearchWorkerUntilIdle(t, newSearchWorker(t, stores, adapter, source, settings))
	requireNodeWarnings(t, stores, fixture.NodeID, nil)
}

// writeUndeclaredSearchNode stores the fixture node with its declared values
// and one value under an undeclared property name.
func writeUndeclaredSearchNode(t *testing.T, stores *fdbadapter.Stores, fixture searchFixture, undeclared string) {
	t.Helper()
	props := map[string]json.RawMessage{
		fixture.IncludedKey: mustJSON("lifecycle text"), fixture.ExcludedKey: mustJSON(readerExcludedValue),
		undeclared: mustJSON("undeclared lifecycle value"),
	}
	now := clock.Now().UTC()
	value := &node.Node{ID: fixture.NodeID, OrgID: fixture.OrgID, NodeType: fixture.TypeKey, Name: searchFixtureName, Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: fixture.NodeID, OrgID: fixture.OrgID, NodeType: fixture.TypeKey, Name: searchFixtureName, Props: props, CreatedAt: now, UpdatedAt: now}
	if err := stores.Nodes.Set(WithTestOperation(t, t.Context()), value, view); err != nil {
		t.Fatalf("store node %s with an undeclared property: %v", fixture.NodeID, err)
	}
}

// requireNodeWarnings checks that nodeID's verified warnings match the
// properties in names exactly and in order.
func requireNodeWarnings(t *testing.T, stores *fdbadapter.Stores, nodeID uuid.UUID, names []string) {
	t.Helper()
	listed := make([]string, 0, len(names))
	cursor := ""
	for {
		page, err := stores.SearchWarnings(WithTestOperation(t, t.Context()), cursor)
		if err != nil {
			t.Fatalf("list search warnings: %v", err)
		}
		for _, warning := range page.Warnings {
			if warning.NodeID == nodeID {
				listed = append(listed, warning.PropertyName)
			}
		}
		if page.Done {
			break
		}
		cursor = page.NextCursor
	}
	if !slices.Equal(listed, names) {
		t.Fatalf("warnings of node %s name %v, want %v", nodeID, listed, names)
	}
}

func searchIndexExists(t *testing.T, control searchControl, index string) bool {
	t.Helper()
	response, err := control.client.Indices.Exists(WithTestOperation(t, t.Context()), &opensearchapi.IndicesExistsReq{Indices: []string{index}})
	if response != nil && response.StatusCode == http.StatusNotFound {
		return false
	}
	if err != nil {
		t.Fatalf("check OpenSearch index %s: %v", index, err)
	}
	return response.StatusCode == http.StatusOK
}
