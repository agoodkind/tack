package integration_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// parentEdgeDeadline bounds each wait for search work or a replacement.
	parentEdgeDeadline = 10 * time.Minute
	// parentEdgeSearchPages bounds the tack_search pages of one traversal.
	parentEdgeSearchPages = 1000
	// parentEdgeSummaryBytes bounds the length of each summary name.
	parentEdgeSummaryBytes = 256
)

func (f parentEdgeFixture) createLeaf(t *testing.T, parentID uuid.UUID, text string) uuid.UUID {
	t.Helper()
	created, err := f.nodes.Create(integration.WithTestOperation(t, t.Context()), service.CreateInput{
		ParentID: parentID, NodeTypeKey: f.kind.typeKey, Name: text, ActorID: f.first.Actors[0].UserID,
		Props: map[string]json.RawMessage{f.kind.included: parentEdgeJSON(t, text), f.kind.excluded: parentEdgeJSON(t, "excluded")},
	})
	if err != nil || created == nil || created.View == nil {
		t.Fatalf("create leaf %q under %s: %v", text, parentID, err)
	}
	return created.View.ID
}

// moveLeaf calls NodeService.Update to set the parent_id and scope_id of
// nodeID to parentID.
func (f parentEdgeFixture) moveLeaf(t *testing.T, nodeID, parentID uuid.UUID) error {
	t.Helper()
	target := parentEdgeJSON(t, parentID.String())
	_, err := f.nodes.Update(integration.WithTestOperation(t, t.Context()), service.UpdateInput{
		NodeID: nodeID, Props: map[string]json.RawMessage{"parent_id": target, "scope_id": target},
		ActorID: f.first.Actors[0].UserID,
	})
	return err
}

// storeNode creates one node of kind with relationships through the node
// store, which schedules the search work of the node.
func (f parentEdgeFixture) storeNode(t *testing.T, kind parentEdgeKind, text string, relationships ...*node.Relationship) uuid.UUID {
	t.Helper()
	nodeID := uuid.Must(uuid.NewV7())
	props := map[string]json.RawMessage{kind.included: parentEdgeJSON(t, text), kind.excluded: parentEdgeJSON(t, "excluded")}
	now := clock.Now().UTC()
	created := &node.Node{ID: nodeID, OrgID: kind.orgID, NodeType: kind.typeKey, Name: text, Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: nodeID, OrgID: kind.orgID, NodeType: kind.typeKey, Name: text, Props: props, CreatedAt: now, UpdatedAt: now}
	for _, relationship := range relationships {
		relationship.OrgID, relationship.SourceID, relationship.CreatedAt = kind.orgID, nodeID, now
	}
	if err := f.stores.Nodes.CreateAtomic(integration.WithTestOperation(t, t.Context()), created, view, relationships, nil, nil, nil); err != nil {
		t.Fatalf("store node %q: %v", text, err)
	}
	return nodeID
}

// summaryKeys returns the summary access keys of each node in nodeIDs.
func (f parentEdgeFixture) summaryKeys(t *testing.T, nodeIDs ...uuid.UUID) map[uuid.UUID][]string {
	t.Helper()
	results, err := f.stores.NodeSummaries(f.stores.SearchPolicySet()).Summaries(integration.WithTestOperation(t, t.Context()), nodeIDs, parentEdgeSummaryBytes)
	if err != nil {
		t.Fatalf("read summaries of %d nodes: %v", len(nodeIDs), err)
	}
	keys := make(map[uuid.UUID][]string, len(results))
	for _, result := range results {
		keys[result.NodeID] = result.AccessKeys
	}
	return keys
}

// index compiles the stable index access of nodeID in the fixture
// organization.
func (f parentEdgeFixture) index(t *testing.T, nodeID uuid.UUID) (node.SearchAccess, error) {
	t.Helper()
	access, err := f.stores.SearchPolicySet().Index(integration.WithTestOperation(t, t.Context()), searchaccess.IndexAccessRequest{
		Version: searchaccess.StableVersion, OrganizationID: f.kind.orgID, ResourceID: nodeID, Generation: 0,
	})
	if err != nil {
		return access, fmt.Errorf("index access of node %s: %w", nodeID, err)
	}
	return access, nil
}

// drain runs search worker slices until the worker cannot claim more work.
// drain fails the test when a slice returns an error or work remains
// claimable for parentEdgeDeadline.
func (f parentEdgeFixture) drain(t *testing.T) {
	t.Helper()
	deadline := clock.Now().Add(parentEdgeDeadline)
	for clock.Now().Before(deadline) {
		claimed, err := f.worker.RunSlice(integration.WithTestOperation(t, t.Context()))
		if err != nil {
			t.Fatalf("search slice failed: %v", err)
		}
		if !claimed {
			return
		}
	}
	t.Fatalf("search work stayed claimable for %s", parentEdgeDeadline)
}

// eventually calls drain and then check until check returns an empty string
// or parentEdgeDeadline (10 minutes) expires, waiting one second between
// attempts. After the deadline, eventually fails the test with the last
// message from check.
func (f parentEdgeFixture) eventually(t *testing.T, check func() string) {
	t.Helper()
	deadline := clock.Now().Add(parentEdgeDeadline)
	message := ""
	for clock.Now().Before(deadline) {
		f.drain(t)
		if message = check(); message == "" {
			return
		}
		<-time.After(time.Second)
	}
	t.Fatalf("%s after %s", message, parentEdgeDeadline)
}

// search follows every tack_search page for query under the entry point of
// workspace and returns the result node IDs.
func (f parentEdgeFixture) search(t *testing.T, workspace datagen.WorkspaceIdentity, query string) []uuid.UUID {
	t.Helper()
	found := []uuid.UUID{}
	cursor := ""
	for range parentEdgeSearchPages {
		result, err := f.driver.Call(integration.WithTestOperation(t, t.Context()), workspace.Actors[0].Token, "tack_search", datagen.ToolArguments{
			WorkspaceReference: workspace.Slug, Query: query, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("search %q under %s: %v", query, workspace.Slug, err)
		}
		cursor = ""
		for _, line := range strings.Split(result.Text(), "\n") {
			if marker := strings.LastIndex(line, "Raw id: `"); marker >= 0 {
				found = append(found, uuid.MustParse(strings.TrimSuffix(line[marker+len("Raw id: `"):], "`")))
			}
			if strings.HasPrefix(line, "Next cursor: `") {
				cursor = strings.TrimSuffix(strings.TrimPrefix(line, "Next cursor: `"), "`")
			}
		}
		if cursor == "" {
			return found
		}
	}
	t.Fatalf("search %q did not finish within %d pages", query, parentEdgeSearchPages)
	return nil
}

func parentEdgeJSON(t *testing.T, value string) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %q: %v", value, err)
	}
	return encoded
}

func intersect[T comparable](values, wanted []T) []T {
	found := []T{}
	for _, value := range values {
		if slices.Contains(wanted, value) {
			found = append(found, value)
		}
	}
	return found
}
